import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:bot_toast/bot_toast.dart';
import 'package:desktop_multi_window/desktop_multi_window.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_hbb/common/widgets/overlay.dart';
import 'package:flutter_hbb/desktop/pages/desktop_tab_page.dart';
import 'package:flutter_hbb/desktop/pages/install_page.dart';
import 'package:flutter_hbb/desktop/pages/server_page.dart';
import 'package:flutter_hbb/desktop/screen/desktop_file_transfer_screen.dart';
import 'package:flutter_hbb/desktop/screen/desktop_view_camera_screen.dart';
import 'package:flutter_hbb/desktop/screen/desktop_port_forward_screen.dart';
import 'package:flutter_hbb/desktop/screen/desktop_remote_screen.dart';
import 'package:flutter_hbb/desktop/screen/desktop_terminal_screen.dart';
import 'package:flutter_hbb/desktop/widgets/refresh_wrapper.dart';
import 'package:flutter_hbb/models/state_model.dart';
import 'package:flutter_hbb/utils/multi_window_manager.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:get/get.dart';
import 'package:provider/provider.dart';
import 'package:window_manager/window_manager.dart';

import 'common.dart';
import 'consts.dart';
import 'mobile/pages/home_page.dart';
import 'mobile/pages/server_page.dart';
import 'mobile/widgets/deploy_dialog.dart';
import 'models/platform_model.dart';

import 'package:flutter_hbb/plugin/handlers.dart'
    if (dart.library.html) 'package:flutter_hbb/web/plugin/handlers.dart';

/// Basic window and launch properties.
int? kWindowId;
WindowType? kWindowType;
late List<String> kBootArgs;

Future<void> main(List<String> args) async {
  earlyAssert();
  WidgetsFlutterBinding.ensureInitialized();

  debugPrint("launch args: $args");
  kBootArgs = List.from(args);

  if (!isDesktop) {
    runMobileApp();
    return;
  }
  // main window
  if (args.isNotEmpty && args.first == 'multi_window') {
    kWindowId = int.parse(args[1]);
    stateGlobal.setWindowId(kWindowId!);
    if (!isMacOS) {
      WindowController.fromWindowId(kWindowId!).showTitleBar(false);
    }
    final argument = args[2].isEmpty
        ? <String, dynamic>{}
        : jsonDecode(args[2]) as Map<String, dynamic>;
    int type = argument['type'] ?? -1;
    // to-do: No need to parse window id ?
    // Because stateGlobal.windowId is a global value.
    argument['windowId'] = kWindowId;
    kWindowType = type.windowType;
    switch (kWindowType) {
      case WindowType.RemoteDesktop:
        desktopType = DesktopType.remote;
        runMultiWindow(
          argument,
          kAppTypeDesktopRemote,
        );
        break;
      case WindowType.FileTransfer:
        desktopType = DesktopType.fileTransfer;
        runMultiWindow(
          argument,
          kAppTypeDesktopFileTransfer,
        );
        break;
      case WindowType.ViewCamera:
        desktopType = DesktopType.viewCamera;
        runMultiWindow(
          argument,
          kAppTypeDesktopViewCamera,
        );
        break;
      case WindowType.PortForward:
        desktopType = DesktopType.portForward;
        runMultiWindow(
          argument,
          kAppTypeDesktopPortForward,
        );
        break;
      case WindowType.Terminal:
        desktopType = DesktopType.terminal;
        runMultiWindow(
          argument,
          kAppTypeDesktopTerminal,
        );
      default:
        break;
    }
  } else if (args.isNotEmpty && args.first == '--cm') {
    debugPrint("--cm started");
    desktopType = DesktopType.cm;
    await windowManager.ensureInitialized();
    runConnectionManagerScreen();
  } else if (args.contains('--install')) {
    runInstallPage();
  } else {
    desktopType = DesktopType.main;
    await windowManager.ensureInitialized();
    windowManager.setPreventClose(true);
    if (isMacOS) {
      disableWindowMovable(kWindowId);
    }
    runMainApp(true);
  }
}

// The boot arguments that name which deployment this client belongs to, mapped
// onto the config key each one writes.
const _kPreConfigOptions = <String, String>{
  '--id-server=': 'custom-rendezvous-server',
  '--api-server=': 'api-server',
  '--relay-server=': 'relay-server',
  '--key=': 'key',
};

// Passed alongside the values above to change an already-configured client.
const _kPreConfigOverwriteArg = '--overwrite-settings';

// The enrollment token is provisioned the same way but is deliberately not one
// of the four above. It names no server, so it cannot re-point a client; it is
// spent and cleared by src/hbbs_http/sync.rs on first use; and it has to reach a
// client whose deployment is already baked in at build time, which the guard
// below would otherwise treat as "already configured" and refuse.
const _kPreConfigEnrollmentArg = '--enrollment-token=';
const _kEnrollmentTokenOption = 'enrollment-token';
const _kDeviceTokenOption = 'device-token';

/// Applies deployment settings passed on the command line.
///
/// These four keys decide which server the client trusts, so anything able to
/// start the binary with arguments could previously re-point an installed
/// client at another deployment, silently and permanently: the old version
/// wrote every matching argument straight into the config with no validation
/// and no regard for what was already there.
///
/// The rule now is first-run only. An unconfigured client takes its settings
/// from the arguments, which is what provisioning needs. A client that already
/// knows its deployment keeps it unless the operator also passes
/// --overwrite-settings, which makes re-pointing a deliberate act rather than a
/// side effect of launching with an extra flag.
Future<void> applyPreConfig() async {
  final requested = <String, String>{};
  var overwrite = false;

  for (final arg in kBootArgs) {
    if (arg == _kPreConfigOverwriteArg) {
      overwrite = true;
      continue;
    }
    if (arg.startsWith(_kPreConfigEnrollmentArg)) {
      final value = arg.substring(_kPreConfigEnrollmentArg.length).trim();
      if (_isValidPreConfigValue(_kEnrollmentTokenOption, value)) {
        requested[_kEnrollmentTokenOption] = value;
      } else {
        debugPrint('applyPreConfig: ignoring $_kPreConfigEnrollmentArg, '
            'value is not usable');
      }
      continue;
    }
    for (final entry in _kPreConfigOptions.entries) {
      if (!arg.startsWith(entry.key)) continue;
      final value = arg.substring(entry.key.length).trim();
      if (!_isValidPreConfigValue(entry.value, value)) {
        debugPrint('applyPreConfig: ignoring ${entry.key}, value is not usable');
        continue;
      }
      requested[entry.value] = value;
    }
  }

  // An APK installed by an MDM or baked into a firmware image is launched by an
  // Intent and never sees a command line, so managed configuration is the only
  // provisioning channel it has. It wins over boot arguments because on that
  // platform it is the authoritative one.
  requested.addAll(await _managedConfig());

  // Before the enrollment token, deliberately: enrollment is where the serial
  // becomes the device's name, and a serial recorded after the fact would leave
  // the device named its RustDesk id until somebody renamed it by hand.
  await _recordSerial();
  await _applyEnrollmentToken(requested.remove(_kEnrollmentTokenOption));
  await _enableStartOnBoot();
  // The service is deliberately NOT started here. applyPreConfig runs inside
  // initEnv, which is before syncAndroidServiceAppDirConfigPath tells the
  // service process where the config directory is. Starting it this early gave
  // the Rust side no config path, so it minted a new RustDesk id on every
  // start and could not persist the device token: the device re-enrolled every
  // heartbeat, changed identity every few minutes, and the portal was left
  // holding an id that no longer existed. runMobileApp starts it once the path
  // is known.

  if (requested.isEmpty) return;

  // Configured means the client already knows its deployment, whether from an
  // earlier run or from settings baked into the build.
  final rendezvous =
      (await bind.mainGetOption(key: 'custom-rendezvous-server')).trim();
  final apiServer = (await bind.mainGetOption(key: 'api-server')).trim();
  final configured = rendezvous.isNotEmpty || apiServer.isNotEmpty;

  for (final entry in requested.entries) {
    final current = (await bind.mainGetOption(key: entry.key)).trim();
    // Re-running provisioning with the same values is not a change, so it does
    // not need consent.
    if (current == entry.value) continue;

    if (configured && !overwrite) {
      debugPrint('applyPreConfig: refusing to change ${entry.key}; this client '
          'is already configured. Pass $_kPreConfigOverwriteArg to override.');
      continue;
    }
    await bind.mainSetOption(key: entry.key, value: entry.value);
  }
}

/// Reads Android managed configuration, keyed by config name.
///
/// The keys are the config keys themselves rather than argument spellings, so
/// what an MDM administrator types matches what the client stores. See
/// android/app/src/main/res/xml/app_restrictions.xml, which is the list an MDM
/// console offers them.
Future<Map<String, String>> _managedConfig() async {
  if (!isAndroid) return const {};
  final allowed = {
    ..._kPreConfigOptions.values,
    _kEnrollmentTokenOption,
  };
  try {
    // platformFFI rather than gFFI, whose invokeMethod is declared Future<bool>
    // and would fail the cast on a map.
    final raw = await platformFFI.invokeMethod(AndroidChannel.kGetManagedConfig);
    if (raw is! Map) return const {};
    final out = <String, String>{};
    raw.forEach((key, value) {
      if (key is! String || value is! String) return;
      if (!allowed.contains(key)) return;
      final trimmed = value.trim();
      if (!_isValidPreConfigValue(key, trimmed)) {
        debugPrint('applyPreConfig: ignoring managed config $key, '
            'value is not usable');
        return;
      }
      out[key] = trimmed;
    });
    return out;
  } catch (err) {
    debugPrint('applyPreConfig: managed configuration unavailable: $err');
    return const {};
  }
}

/// Stores an enrollment token, but only while the client is still unenrolled.
///
/// A token is spent on redemption and cleared by the Rust side, so re-writing
/// one on a device that already holds a device credential would leave a live
/// credential sitting in the config of every deployed device for no gain. This
/// deliberately does not consult the already-configured guard: a build with its
/// deployment baked in is configured by definition, and refusing the token there
/// would mean no locked build could ever enrol.
Future<void> _applyEnrollmentToken(String? token) async {
  if (token == null) return;
  final deviceToken = (await bind.mainGetOption(key: _kDeviceTokenOption)).trim();
  if (deviceToken.isNotEmpty) {
    debugPrint('applyPreConfig: ignoring enrollment token; this client is '
        'already enrolled');
    return;
  }
  final current =
      (await bind.mainGetOption(key: _kEnrollmentTokenOption)).trim();
  if (current == token) return;
  await bind.mainSetOption(key: _kEnrollmentTokenOption, value: token);
}

// The identifier a technician searches by, read by src/hbbs_http/sync.rs and
// sent at enrollment.
const _kSerialOption = 'odv-serial';

/// Records the device's serial number, once.
///
/// The value comes from the platform: on Android an MDM asset tag, then the
/// hardware serial, then ANDROID_ID (see common.kt:deviceSerial). Elsewhere
/// there is no source and the field stays empty, which the server accepts.
///
/// Written once rather than refreshed: the serial is what the device is named
/// after and what a technician searches on, so a value that moved would break
/// the association the moment it mattered.
Future<void> _recordSerial() async {
  if (!isAndroid) return;
  final current = (await bind.mainGetOption(key: _kSerialOption)).trim();
  if (current.isNotEmpty) return;
  try {
    final serial = await platformFFI.invokeMethod(AndroidChannel.kGetDeviceSerial);
    if (serial is! String || serial.trim().isEmpty) return;
    await bind.mainSetOption(key: _kSerialOption, value: serial.trim());
  } catch (err) {
    debugPrint('applyPreConfig: could not read the device serial: $err');
  }
}

// Marks that provisioning has already had its one chance to turn start on boot
// on, so an operator who turns it back off keeps that decision.
const _kStartOnBootProvisionedOption = 'odv-start-on-boot-provisioned';

/// Turns on start on boot for a client that belongs to a deployment.
///
/// Android's receiver defaults this to false, and nothing set it, so a
/// preconfigured client stayed off after every reboot until somebody opened the
/// app and toggled it. That is the one thing an unattended deployment cannot
/// ask for.
///
/// Done once rather than on every launch: this is provisioning, not policy, and
/// re-asserting it would silently undo an operator who turned it off.
/// Starts the background service on a client that knows its deployment.
///
/// Without this the service only ever starts from BootReceiver or from somebody
/// tapping "share screen", so a freshly installed device sat there having never
/// contacted the server: no heartbeat, no enrolment, nothing in the portal. A
/// fleet nobody is supposed to touch cannot depend on that tap.
///
/// The service is started on the same headless path a boot uses, so it does not
/// raise a consent dialog; screen capture comes from the PROJECT_MEDIA app op
/// granted at provisioning, not from asking whoever is holding the device.
Future<void> _startServiceUnattended() async {
  if (!isAndroid) return;
  final rendezvous =
      (await bind.mainGetOption(key: 'custom-rendezvous-server')).trim();
  final apiServer = (await bind.mainGetOption(key: 'api-server')).trim();
  if (rendezvous.isEmpty && apiServer.isEmpty) return;

  try {
    await platformFFI.invokeMethod(AndroidChannel.kStartServiceHeadless);
  } catch (err) {
    debugPrint('applyPreConfig: could not start the service: $err');
  }
}

Future<void> _enableStartOnBoot() async {
  if (!isAndroid) return;
  final already =
      (await bind.mainGetOption(key: _kStartOnBootProvisionedOption)).trim();
  if (already == 'Y') return;

  // Only for a client that knows which deployment it belongs to. An unlocked
  // build is somebody's own copy and gets upstream's behaviour.
  final rendezvous =
      (await bind.mainGetOption(key: 'custom-rendezvous-server')).trim();
  final apiServer = (await bind.mainGetOption(key: 'api-server')).trim();
  if (rendezvous.isEmpty && apiServer.isEmpty) return;

  try {
    await platformFFI.invokeMethod(AndroidChannel.kSetStartOnBootOpt, true);
    await bind.mainSetOption(key: _kStartOnBootProvisionedOption, value: 'Y');
  } catch (err) {
    debugPrint('applyPreConfig: could not enable start on boot: $err');
  }
}

bool _isValidPreConfigValue(String key, String value) {
  if (value.isEmpty) return false;
  // Whitespace and control characters cannot appear in a host, a URL or a
  // public key, and are how one argument becomes two config entries.
  if (value.contains(RegExp(r'[\s\x00-\x1f]'))) return false;

  if (key == 'api-server') {
    final uri = Uri.tryParse(value);
    if (uri == null || !uri.isAbsolute) return false;
    if (uri.scheme != 'http' && uri.scheme != 'https') return false;
    if (uri.scheme == 'http') {
      // Not refused: a lab deployment on a private network is a legitimate
      // case. It is called out because the heartbeat response carries
      // config_options, so plain HTTP here is a channel that can reprogram
      // this client.
      debugPrint('applyPreConfig: api-server is plain HTTP, so the heartbeat '
          'response that can rewrite this client\'s configuration is not '
          'protected in transit');
    }
  }

  return true;
}

Future<void> initEnv(String appType) async {
  // global shared preference
  await platformFFI.init(appType);
  // global FFI, use this **ONLY** for global configuration
  // for convenience, use global FFI on mobile platform
  // focus on multi-ffi on desktop first
  await initGlobalFFI();
  // Apply pre-configuration from boot args (e.g. --id-server=, --api-server=)
  if (appType == kAppTypeMain) {
    await applyPreConfig();
  }
  // await Firebase.initializeApp();
  _registerEventHandler();
  // Update the system theme.
  updateSystemWindowTheme();
}

void runMainApp(bool startService) async {
  // register uni links
  await initEnv(kAppTypeMain);
  checkUpdate();
  // trigger connection status updater
  await bind.mainCheckConnectStatus();
  if (startService) {
    gFFI.serverModel.startService();
    bind.pluginSyncUi(syncTo: kAppTypeMain);
    bind.pluginListReload();
  }
  await Future.wait([gFFI.abModel.loadCache(), gFFI.groupModel.loadCache()]);
  gFFI.userModel.refreshCurrentUser();
  runApp(App());

  bool? alwaysOnTop;
  if (isDesktop) {
    alwaysOnTop =
        bind.mainGetBuildinOption(key: "main-window-always-on-top") == 'Y';
  }

  // Set window option.
  WindowOptions windowOptions = getHiddenTitleBarWindowOptions(
      isMainWindow: true, alwaysOnTop: alwaysOnTop);
  windowManager.waitUntilReadyToShow(windowOptions, () async {
    // Restore the location of the main window before window hide or show.
    await restoreWindowPosition(WindowType.Main);
    // Check the startup argument, if we successfully handle the argument, we keep the main window hidden.
    final handledByUniLinks = await initUniLinks();
    debugPrint("handled by uni links: $handledByUniLinks");
    if (handledByUniLinks || handleUriLink(cmdArgs: kBootArgs)) {
      windowManager.hide();
    } else {
      windowManager.show();
      windowManager.focus();
      // Move registration of active main window here to prevent from async visible check.
      rustDeskWinManager.registerActiveWindow(kWindowMainId);
    }
    windowManager.setOpacity(1);
    windowManager.setTitle(getWindowName());
    // Do not use `windowManager.setResizable()` here.
    setResizable(!bind.isIncomingOnly());
  });
}

void runMobileApp() async {
  await initEnv(kAppTypeMain);
  checkUpdate();
  if (isAndroid) androidChannelInit();
  if (isAndroid) platformFFI.syncAndroidServiceAppDirConfigPath();
  // Only now does the service know where to read and write config, so this is
  // the earliest point at which it can keep an identity across restarts.
  if (isAndroid) await _startServiceUnattended();
  draggablePositions.load();
  await Future.wait([gFFI.abModel.loadCache(), gFFI.groupModel.loadCache()]);
  gFFI.userModel.refreshCurrentUser();
  runApp(App());
  await initUniLinks();
}

void runMultiWindow(
  Map<String, dynamic> argument,
  String appType,
) async {
  await initEnv(appType);
  final title = getWindowName();
  // set prevent close to true, we handle close event manually
  WindowController.fromWindowId(kWindowId!).setPreventClose(true);
  if (isMacOS) {
    disableWindowMovable(kWindowId);
  }
  late Widget widget;
  switch (appType) {
    case kAppTypeDesktopRemote:
      draggablePositions.load();
      widget = DesktopRemoteScreen(
        params: argument,
      );
      break;
    case kAppTypeDesktopFileTransfer:
      widget = DesktopFileTransferScreen(
        params: argument,
      );
      break;
    case kAppTypeDesktopViewCamera:
      draggablePositions.load();
      widget = DesktopViewCameraScreen(
        params: argument,
      );
      break;
    case kAppTypeDesktopPortForward:
      widget = DesktopPortForwardScreen(
        params: argument,
      );
      break;
    case kAppTypeDesktopTerminal:
      widget = DesktopTerminalScreen(
        params: argument,
      );
      break;
    default:
      // no such appType
      exit(0);
  }
  _runApp(
    title,
    widget,
    MyTheme.currentThemeMode(),
  );
  // we do not hide titlebar on win7 because of the frame overflow.
  if (kUseCompatibleUiMode) {
    WindowController.fromWindowId(kWindowId!).showTitleBar(true);
  }
  switch (appType) {
    case kAppTypeDesktopRemote:
      // If screen rect is set, the window will be moved to the target screen and then set fullscreen.
      if (argument['screen_rect'] == null) {
        // display can be used to control the offset of the window.
        await restoreWindowPosition(
          WindowType.RemoteDesktop,
          windowId: kWindowId!,
          peerId: argument['id'] as String?,
          display: argument['display'] as int?,
        );
      }
      break;
    case kAppTypeDesktopFileTransfer:
      await restoreWindowPosition(WindowType.FileTransfer,
          windowId: kWindowId!);
      break;
    case kAppTypeDesktopViewCamera:
      // If screen rect is set, the window will be moved to the target screen and then set fullscreen.
      if (argument['screen_rect'] == null) {
        // display can be used to control the offset of the window.
        await restoreWindowPosition(
          WindowType.ViewCamera,
          windowId: kWindowId!,
          peerId: argument['id'] as String?,
          // FIXME: fix display index.
          display: argument['display'] as int?,
        );
      }
      break;
    case kAppTypeDesktopPortForward:
      await restoreWindowPosition(WindowType.PortForward, windowId: kWindowId!);
      break;
    case kAppTypeDesktopTerminal:
      await restoreWindowPosition(WindowType.Terminal, windowId: kWindowId!);
      break;
    default:
      // no such appType
      exit(0);
  }
  // show window from hidden status
  WindowController.fromWindowId(kWindowId!).show();
}

void runConnectionManagerScreen() async {
  await initEnv(kAppTypeConnectionManager);
  _runApp(
    '',
    const DesktopServerPage(),
    MyTheme.currentThemeMode(),
  );
  final hide = await bind.cmGetConfig(name: "hide_cm") == 'true';
  gFFI.serverModel.hideCm = hide;
  if (hide) {
    await hideCmWindow(isStartup: true);
  } else {
    await showCmWindow(isStartup: true);
  }
  setResizable(false);
  // Start the uni links handler and redirect links to Native, not for Flutter.
  listenUniLinks(handleByFlutter: false);
}

bool _isCmReadyToShow = false;

showCmWindow({bool isStartup = false}) async {
  if (isStartup) {
    WindowOptions windowOptions = getHiddenTitleBarWindowOptions(
        size: kConnectionManagerWindowSizeClosedChat, alwaysOnTop: true);
    await windowManager.waitUntilReadyToShow(windowOptions, null);
    bind.mainHideDock();
    await Future.wait([
      windowManager.show(),
      windowManager.focus(),
      windowManager.setOpacity(1)
    ]);
    // ensure initial window size to be changed
    await windowManager.setSizeAlignment(
        kConnectionManagerWindowSizeClosedChat, Alignment.topRight);
    _isCmReadyToShow = true;
  } else if (_isCmReadyToShow) {
    if (await windowManager.getOpacity() != 1) {
      await windowManager.setOpacity(1);
      await windowManager.focus();
      await windowManager.minimize(); //needed
      await windowManager.setSizeAlignment(
          kConnectionManagerWindowSizeClosedChat, Alignment.topRight);
      windowOnTop(null);
    }
  }
}

hideCmWindow({bool isStartup = false}) async {
  if (isStartup) {
    WindowOptions windowOptions = getHiddenTitleBarWindowOptions(
        size: kConnectionManagerWindowSizeClosedChat);
    windowManager.setOpacity(0);
    await windowManager.waitUntilReadyToShow(windowOptions, null);
    bind.mainHideDock();
    await windowManager.minimize();
    await windowManager.hide();
    _isCmReadyToShow = true;
  } else if (_isCmReadyToShow) {
    if (await windowManager.getOpacity() != 0) {
      await windowManager.setOpacity(0);
      bind.mainHideDock();
      await windowManager.minimize();
      await windowManager.hide();
    }
  }
}

void _runApp(
  String title,
  Widget home,
  ThemeMode themeMode,
) {
  final botToastBuilder = BotToastInit();
  runApp(RefreshWrapper(
    builder: (context) => GetMaterialApp(
      navigatorKey: globalKey,
      debugShowCheckedModeBanner: false,
      title: title,
      theme: MyTheme.lightTheme,
      darkTheme: MyTheme.darkTheme,
      themeMode: themeMode,
      home: home,
      localizationsDelegates: const [
        GlobalMaterialLocalizations.delegate,
        GlobalWidgetsLocalizations.delegate,
        GlobalCupertinoLocalizations.delegate,
      ],
      supportedLocales: supportedLocales,
      navigatorObservers: [
        // FirebaseAnalyticsObserver(analytics: analytics),
        BotToastNavigatorObserver(),
      ],
      builder: (context, child) {
        child = _keepScaleBuilder(context, child);
        child = botToastBuilder(context, child);
        return child;
      },
    ),
  ));
}

void runInstallPage() async {
  await windowManager.ensureInitialized();
  await initEnv(kAppTypeMain);
  _runApp('', const InstallPage(), MyTheme.currentThemeMode());
  WindowOptions windowOptions =
      getHiddenTitleBarWindowOptions(size: Size(800, 600), center: true);
  windowManager.waitUntilReadyToShow(windowOptions, () async {
    windowManager.show();
    windowManager.focus();
    windowManager.setOpacity(1);
    windowManager.setAlignment(Alignment.center); // ensure
  });
}

WindowOptions getHiddenTitleBarWindowOptions(
    {bool isMainWindow = false,
    Size? size,
    bool center = false,
    bool? alwaysOnTop}) {
  var defaultTitleBarStyle = TitleBarStyle.hidden;
  // we do not hide titlebar on win7 because of the frame overflow.
  if (kUseCompatibleUiMode) {
    defaultTitleBarStyle = TitleBarStyle.normal;
  }
  return WindowOptions(
    size: size,
    center: center,
    backgroundColor: (isMacOS && isMainWindow) ? null : Colors.transparent,
    skipTaskbar: false,
    titleBarStyle: defaultTitleBarStyle,
    alwaysOnTop: alwaysOnTop,
  );
}

class App extends StatefulWidget {
  @override
  State<App> createState() => _AppState();
}

class _AppState extends State<App> with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.window.onPlatformBrightnessChanged = () {
      final userPreference = MyTheme.getThemeModePreference();
      if (userPreference != ThemeMode.system) return;
      WidgetsBinding.instance.handlePlatformBrightnessChanged();
      final systemIsDark =
          WidgetsBinding.instance.platformDispatcher.platformBrightness ==
              Brightness.dark;
      final ThemeMode to;
      if (systemIsDark) {
        to = ThemeMode.dark;
      } else {
        to = ThemeMode.light;
      }
      Get.changeThemeMode(to);
      // Synchronize the window theme of the system.
      updateSystemWindowTheme();
      if (desktopType == DesktopType.main) {
        bind.mainChangeTheme(dark: to.toShortString());
      }
    };
    WidgetsBinding.instance.addObserver(this);
    WidgetsBinding.instance.addPostFrameCallback((_) => _updateOrientation());
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeMetrics() {
    _updateOrientation();
  }

  void _updateOrientation() {
    if (isDesktop) return;

    // Don't use `MediaQuery.of(context).orientation` in `didChangeMetrics()`,
    // my test (Flutter 3.19.6, Android 14) is always the reverse value.
    // https://github.com/flutter/flutter/issues/60899
    // stateGlobal.isPortrait.value =
    //     MediaQuery.of(context).orientation == Orientation.portrait;

    final orientation = View.of(context).physicalSize.aspectRatio > 1
        ? Orientation.landscape
        : Orientation.portrait;
    stateGlobal.isPortrait.value = orientation == Orientation.portrait;
  }

  @override
  Widget build(BuildContext context) {
    // final analytics = FirebaseAnalytics.instance;
    final botToastBuilder = BotToastInit();
    return RefreshWrapper(builder: (context) {
      return MultiProvider(
        providers: [
          // global configuration
          // use session related FFI when in remote control or file transfer page
          ChangeNotifierProvider.value(value: gFFI.ffiModel),
          ChangeNotifierProvider.value(value: gFFI.imageModel),
          ChangeNotifierProvider.value(value: gFFI.cursorModel),
          ChangeNotifierProvider.value(value: gFFI.canvasModel),
          ChangeNotifierProvider.value(value: gFFI.peerTabModel),
        ],
        child: GetMaterialApp(
          navigatorKey: globalKey,
          debugShowCheckedModeBanner: false,
          title: isWeb
              ? '${bind.mainGetAppNameSync()} Web Client V2 (Preview)'
              : bind.mainGetAppNameSync(),
          theme: MyTheme.lightTheme,
          darkTheme: MyTheme.darkTheme,
          themeMode: MyTheme.currentThemeMode(),
          home: isDesktop
              ? const DesktopTabPage()
              : isWeb
                  ? WebHomePage()
                  : HomePage(),
          localizationsDelegates: const [
            GlobalMaterialLocalizations.delegate,
            GlobalWidgetsLocalizations.delegate,
            GlobalCupertinoLocalizations.delegate,
          ],
          supportedLocales: supportedLocales,
          navigatorObservers: [
            // FirebaseAnalyticsObserver(analytics: analytics),
            BotToastNavigatorObserver(),
          ],
          builder: isAndroid
              ? (context, child) => AccessibilityListener(
                    child: MediaQuery(
                      data: MediaQuery.of(context).copyWith(
                        textScaler: TextScaler.linear(1.0),
                      ),
                      child: child ?? Container(),
                    ),
                  )
              : (context, child) {
                  child = _keepScaleBuilder(context, child);
                  child = botToastBuilder(context, child);
                  if ((isDesktop && desktopType == DesktopType.main) ||
                      isWebDesktop) {
                    child = keyListenerBuilder(context, child);
                  }
                  if (isLinux) {
                    return buildVirtualWindowFrame(context, child);
                  } else {
                    return workaroundWindowBorder(context, child);
                  }
                },
        ),
      );
    });
  }
}

Widget _keepScaleBuilder(BuildContext context, Widget? child) {
  return MediaQuery(
    data: MediaQuery.of(context).copyWith(
      textScaler: TextScaler.linear(1.0),
    ),
    child: child ?? Container(),
  );
}

_registerEventHandler() {
  if (isDesktop && desktopType != DesktopType.main) {
    platformFFI.registerEventHandler('theme', 'theme', (evt) async {
      String? dark = evt['dark'];
      if (dark != null) {
        await MyTheme.changeDarkMode(MyTheme.themeModeFromString(dark));
      }
    });
    platformFFI.registerEventHandler('language', 'language', (_) async {
      reloadAllWindows();
    });
  }
  // Register native handlers.
  if (isDesktop) {
    platformFFI.registerEventHandler('native_ui', 'native_ui', (evt) async {
      NativeUiHandler.instance.onEvent(evt);
    });
  }
  if (isAndroid) {
    platformFFI.registerEventHandler(
        'android_needs_deploy', 'android_needs_deploy', (_) async {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        showDeployPromptDialog();
      });
    });
  }
}

Widget keyListenerBuilder(BuildContext context, Widget? child) {
  return RawKeyboardListener(
    // `skipTraversal: isWeb` is to fix "Bad state: RenderBox was not laid out: minified:aeL#c19e4"
    focusNode: FocusNode(skipTraversal: isWeb),
    child: child ?? Container(),
    onKey: (RawKeyEvent event) {
      if (event.logicalKey == LogicalKeyboardKey.shiftLeft) {
        if (event is RawKeyDownEvent) {
          gFFI.peerTabModel.setShiftDown(true);
        } else if (event is RawKeyUpEvent) {
          gFFI.peerTabModel.setShiftDown(false);
        }
      }
    },
  );
}
