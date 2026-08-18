package com.carriez.flutter_hbb

import android.Manifest.permission.*
import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.media.AudioRecord
import android.media.AudioRecord.READ_BLOCKING
import android.media.MediaCodecList
import android.media.MediaFormat
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.Settings
import android.provider.Settings.*
import android.util.DisplayMetrics
import android.util.Log
import android.view.WindowManager
import androidx.annotation.RequiresApi
import androidx.core.content.ContextCompat.getSystemService
import com.hjq.permissions.Permission
import com.hjq.permissions.XXPermissions
import ffi.FFI
import java.nio.ByteBuffer
import java.util.*


// intent action, extra
// AppOpsManager.OPSTR_PROJECT_MEDIA is @hide, so the op is named directly.
// Granting it (appops set <package> PROJECT_MEDIA allow) is what lets a
// provisioned device capture with nobody present, across reboots.
const val OPSTR_PROJECT_MEDIA = "android:project_media"

const val ACT_REQUEST_MEDIA_PROJECTION = "REQUEST_MEDIA_PROJECTION"
const val ACT_INIT_MEDIA_PROJECTION_AND_SERVICE = "INIT_MEDIA_PROJECTION_AND_SERVICE"
const val ACT_LOGIN_REQ_NOTIFY = "LOGIN_REQ_NOTIFY"
const val EXT_INIT_FROM_BOOT = "EXT_INIT_FROM_BOOT"
// Set when a person launched the app, so MainService may raise the system
// consent dialog if the PROJECT_MEDIA op has not been granted. A boot start
// never sets it: nobody is there to answer.
const val EXT_ALLOW_CONSENT_PROMPT = "EXT_ALLOW_CONSENT_PROMPT"
const val EXT_MEDIA_PROJECTION_RES_INTENT = "MEDIA_PROJECTION_RES_INTENT"
const val EXT_LOGIN_REQ_NOTIFY = "LOGIN_REQ_NOTIFY"

// Activity requestCode
const val REQ_INVOKE_PERMISSION_ACTIVITY_MEDIA_PROJECTION = 101
const val REQ_REQUEST_MEDIA_PROJECTION = 201

// Activity responseCode
const val RES_FAILED = -100

// Flutter channel
const val START_ACTION = "start_action"
const val GET_START_ON_BOOT_OPT = "get_start_on_boot_opt"
const val SET_START_ON_BOOT_OPT = "set_start_on_boot_opt"
// Starts MainService headlessly, on the same path BootReceiver uses, so a
// provisioned client registers on first launch without anyone tapping "share
// screen" and without a consent dialog.
const val START_SERVICE_HEADLESS = "start_service_headless"
const val SYNC_APP_DIR_CONFIG_PATH = "sync_app_dir"
const val GET_VALUE = "get_value"
const val GET_MANAGED_CONFIG = "get_managed_config"
const val GET_DEVICE_SERIAL = "get_device_serial"

// Managed configuration key for an asset tag pushed by the MDM. Preferred over
// the hardware serial because it is the identifier the customer's own systems
// already use, and because the hardware serial is not readable by an ordinary
// app on Android 10 and later.
const val KEY_SERIAL_NUMBER = "serial-number"

const val KEY_IS_SUPPORT_VOICE_CALL = "KEY_IS_SUPPORT_VOICE_CALL"

const val KEY_SHARED_PREFERENCES = "KEY_SHARED_PREFERENCES"
const val KEY_START_ON_BOOT_OPT = "KEY_START_ON_BOOT_OPT"
const val KEY_APP_DIR_CONFIG_PATH = "KEY_APP_DIR_CONFIG_PATH"

@SuppressLint("ConstantLocale")
val LOCAL_NAME = Locale.getDefault().toString()
val SCREEN_INFO = Info(0, 0, 1, 200)

data class Info(
    var width: Int, var height: Int, var scale: Int, var dpi: Int
)

fun isSupportVoiceCall(): Boolean {
    // https://developer.android.com/reference/android/media/MediaRecorder.AudioSource#VOICE_COMMUNICATION
    return Build.VERSION.SDK_INT >= Build.VERSION_CODES.R
}

fun requestPermission(context: Context, type: String) {
    XXPermissions.with(context)
        .permission(type)
        .request { _, all ->
            if (all) {
                Handler(Looper.getMainLooper()).post {
                    MainActivity.flutterMethodChannel?.invokeMethod(
                        "on_android_permission_result",
                        mapOf("type" to type, "result" to all)
                    )
                }
            }
        }
}

fun startAction(context: Context, action: String) {
    try {
        context.startActivity(Intent(action).apply {
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            // don't pass package name when launch ACTION_ACCESSIBILITY_SETTINGS
            if (ACTION_ACCESSIBILITY_SETTINGS != action) {
                data = Uri.parse("package:" + context.packageName)
            }
        })
    } catch (e: Exception) {
        e.printStackTrace()
    }
}

class AudioReader(val bufSize: Int, private val maxFrames: Int) {
    private var currentPos = 0
    private val bufferPool: Array<ByteBuffer>

    init {
        if (maxFrames < 0 || maxFrames > 32) {
            throw Exception("Out of bounds")
        }
        if (bufSize <= 0) {
            throw Exception("Wrong bufSize")
        }
        bufferPool = Array(maxFrames) {
            ByteBuffer.allocateDirect(bufSize)
        }
    }

    private fun next() {
        currentPos++
        if (currentPos >= maxFrames) {
            currentPos = 0
        }
    }

    @RequiresApi(Build.VERSION_CODES.M)
    fun readSync(audioRecord: AudioRecord): ByteBuffer? {
        val buffer = bufferPool[currentPos]
        val res = audioRecord.read(buffer, bufSize, READ_BLOCKING)
        return if (res > 0) {
            next()
            buffer
        } else {
            null
        }
    }
}


fun getScreenSize(windowManager: WindowManager) : Pair<Int, Int>{
    var w = 0
    var h = 0
    @Suppress("DEPRECATION")
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
        val m = windowManager.maximumWindowMetrics
        w = m.bounds.width()
        h = m.bounds.height()
    } else {
        val dm = DisplayMetrics()
        windowManager.defaultDisplay.getRealMetrics(dm)
        w = dm.widthPixels
        h = dm.heightPixels
    }
    return Pair(w, h)
}

 fun translate(input: String): String {
    Log.d("common", "translate:$LOCAL_NAME")
    return FFI.translateLocale(LOCAL_NAME, input)
}

// OpenDeskViewer: provisioning for a device that has no command line.
//
// An APK pushed by an MDM or baked into a firmware image is started by an
// Intent, so the boot arguments the desktop client is provisioned with never
// arrive. Managed configuration is the channel every MDM already speaks; the
// keys are declared in res/xml/app_restrictions.xml and are the config keys
// themselves, so what an administrator types is what the client stores.
// OpenDeskViewer: the identifier a technician searches a device by.
//
// Three sources, in preference order, because which one is available is a
// property of how the fleet was deployed rather than something the app decides:
//
//   1. an asset tag from managed configuration, which is what an MDM can push
//      and what the customer's own systems already know the device by;
//   2. the hardware serial, readable only by a privileged or device-owner app
//      since Android 10, so this is the firmware-preinstall case;
//   3. ANDROID_ID, which is stable per device and app-signing key but is not a
//      manufacturer serial. It is the fallback so that a device is always
//      findable by something more durable than its RustDesk id.
//
// Which one a given fleet carries has to be recorded in the deployment spec,
// because it changes what a technician types into the search box.
fun deviceSerial(context: Context): String {
    managedConfig(context)[KEY_SERIAL_NUMBER]?.let { return it }

    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
        try {
            val serial = Build.getSerial()
            if (serial.isNotEmpty() && serial != Build.UNKNOWN) {
                return serial
            }
        } catch (e: SecurityException) {
            Log.d("common", "hardware serial is not readable by this app")
        }
    }

    return Settings.Secure.getString(context.contentResolver, Settings.Secure.ANDROID_ID)
        ?: ""
}

fun managedConfig(context: Context): Map<String, String> {
    val manager = context.getSystemService(Context.RESTRICTIONS_SERVICE)
            as? android.content.RestrictionsManager ?: return emptyMap()
    val restrictions = manager.applicationRestrictions ?: return emptyMap()
    val out = mutableMapOf<String, String>()
    for (key in restrictions.keySet()) {
        val value = restrictions.getString(key)?.trim()
        if (!value.isNullOrEmpty()) {
            out[key] = value
        }
    }
    return out
}