# OpenDeskViewer Android Build

This directory contains Android-specific configuration for the OpenDeskViewer rebranded Flutter app.

## Prerequisites

- Flutter 3.22.3
- Android SDK with NDK r28c
- Rust 1.75
- cargo-ndk 3.1.2

## Build Requirements

The Android build requires:
1. Flutter environment with Android SDK
2. NDK r28c for Rust compilation
3. vcpkg at pinned commit for native dependencies

## Build Steps

### Local Build

```bash
cd flutter
flutter build apk --release --obfuscate --split-debug-info=../build/app
```

### CI Build

The CI workflow at `.github/workflows/odv-android.yml` handles:
- NDK setup (r28c)
- Rust toolchain (1.75)
- vcpkg dependencies (ffmpeg, aom, libvpx, opus, oboe)
- Flutter codegen bridge
- Signing with release keystore

## Signing Configuration

Create `flutter/android/key.properties` with:

```properties
storePassword=your-store-password
keyPassword=your-key-password
keyAlias=odv-key
storeFile=../android/keys/odv-key.jks
```

## Deep Linking

The app supports `rustdesk://` deep links. The scheme is auto-derived from the app name.

## APK Output

Build output: `flutter/build/app/outputs/flutter-apk/app-release.apk`

## Distribution

Signed APKs can be distributed via:
- Direct download from your server
- Internal app stores
- Enterprise distribution platforms

## Troubleshooting

### Build fails with NDK errors
Ensure NDK r28c is installed:
```bash
sdkmanager "ndk;28.0.13004108"
```

### vcpkg errors
Use the pinned vcpkg commit from flutter-build.yml

### Signing errors
Verify `key.properties` path and keystore file exist
