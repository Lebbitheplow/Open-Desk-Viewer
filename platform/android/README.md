# OpenDeskViewer Android client

The client is built from the Flutter/Rust tree at the repository root. This directory holds the
Android-specific deployment material: the signing key template and this note.

For what a firmware image or MDM console has to provide, see the **Deploying the client** section
of `platform/README.md`. That is the deployment contract; this is the build.

## Toolchain

The versions that matter are pinned in `.github/workflows/odv-android.yml` and are the ones a
local build should match. As of now:

| Piece | Version |
|---|---|
| Flutter | 3.24.0 |
| Rust | 1.82.0 |
| NDK | r28c |
| cargo-ndk | 3.1.2 |
| flutter_rust_bridge_codegen | 1.80.1, matching the `=1.80` pin in `Cargo.toml` |
| vcpkg | the commit in `VCPKG_COMMIT_ID` |

The workflow is the source of truth. This table is a convenience and has been wrong before: it
said Flutter 3.22.3 and Rust 1.75 against a workflow that pinned neither.

## Building

`flutter build apk` alone is not enough: the APK loads `librustdesk.so`, which nothing in the
Flutter build produces. In order:

```bash
# Native dependencies (libvpx, libyuv, opus, aom, oboe), per ABI
./flutter/build_android_deps.sh arm64-v8a

# The generated bridge. generated_bridge.dart is gitignored, and
# `dart run build_runner` is not this project's generator.
flutter_rust_bridge_codegen --rust-input ./src/flutter_ffi.rs \
    --dart-output ./flutter/lib/generated_bridge.dart

# librustdesk.so, into the jniLibs directory the APK reads
./flutter/ndk_arm64.sh
mkdir -p flutter/android/app/src/main/jniLibs/arm64-v8a
cp target/aarch64-linux-android/release/liblibrustdesk.so \
   flutter/android/app/src/main/jniLibs/arm64-v8a/librustdesk.so

cd flutter && flutter build apk --release --target-platform android-arm64
```

The deployment identity is compiled in from `ODV_RENDEZVOUS_SERVER`, `ODV_RELAY_SERVER`,
`ODV_API_SERVER` and `ODV_RS_PUB_KEY`. A build without them produces a client that points at
RustDesk's own servers. `.github/scripts/check-client-config.sh --verify` greps the shared object
inside the built APK for the baked-in API server and fails if it is not there, which is the only
check that says the compiler actually saw the variables.

## Naming, and what is deliberately still called RustDesk

Two different names are in play, and conflating them is what the old `rebrand.sh` did.

**The customer-visible name is OpenDeskViewer.** `app_name` in
`flutter/android/app/src/main/res/values/strings.xml`, referenced by `android:label` in the
manifest, plus `applicationId "com.opendeskviewer.client"` in `app/build.gradle`. That covers the
launcher, the app info screen, the accessibility settings entry and the id an MDM addresses.

**The protocol-level name is still RustDesk, on purpose.** `config::APP_NAME` stays `"RustDesk"`,
which means:

- the deep link stays `rustdesk://`, which is what `POST /api/v1/devices/{id}/connect` returns and
  what the manifest registers. Changing one end without the other breaks the technician's
  click-to-connect, and changing both buys nothing the requirements ask for;
- the on-disk config paths keep their names;
- `common.rs:is_custom_client()` stays false, which is correct: it gates upstream's signed
  custom-client config feature, which this deployment does not use.

**The Kotlin package stays `com.carriez.flutter_hbb`.** The Rust calls into `MainService`,
`InputService` and `FFI` through JNI by fully qualified class name. `rebrand.sh` moved that
directory in place, which produces a build that compiles and fails at runtime; it was also called
by no workflow and seded an `android:label="flutter_hbb"` the manifest has never had. It was
deleted rather than fixed: `applicationId` and a string resource do the whole job declaratively.

## Signing

CI signs from `ANDROID_SIGNING_KEY`, `ANDROID_ALIAS`, `ANDROID_KEY_STORE_PASSWORD` and
`ANDROID_KEY_PASSWORD`. With none set the workflow raises a warning annotation and uploads a
debug-signed APK, which is fine for a look and useless for a deployment: an MDM cannot push an
update whose signing identity changed, and a firmware preinstall needs the key decided before the
image is cut.

For a local release build, copy `key.properties.example` to `flutter/android/key.properties`.
