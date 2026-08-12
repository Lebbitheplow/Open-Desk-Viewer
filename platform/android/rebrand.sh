#!/bin/bash

# OpenDeskViewer Android Rebrand Script
# This script rebrands the Flutter-based Android app for OpenDeskViewer

set -e

echo "=== OpenDeskViewer Android Rebrand ==="
echo ""

# Configuration
APP_NAME="OpenDeskViewer"
APPLICATION_ID="com.opendeskviewer.android"
PACKAGE_NAME="com/opendeskviewer/android"

# Paths
FLUTTER_DIR="${FLUTTER_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && cd ../flutter && pwd)}"
ANDROID_DIR="${FLUTTER_DIR}/android"

if [ ! -d "$ANDROID_DIR" ]; then
    echo "ERROR: Android directory not found at $ANDROID_DIR"
    exit 1
fi

cd "$ANDROID_DIR"

# 1. Update Application ID in build.gradle
echo "Updating Application ID in app/build.gradle..."
sed -i 's/com\.carriez\.flutter_hbb/'"$APPLICATION_ID"'/' app/build.gradle

# 2. Update AndroidManifest.xml
echo "Updating AndroidManifest.xml..."
MANIFEST_FILE="app/src/main/AndroidManifest.xml"

if [ -f "$MANIFEST_FILE" ]; then
    # Update package
    sed -i 's/package="com\.carriez\.flutter_hbb"/package="'"$APPLICATION_ID"'"/' "$MANIFEST_FILE"

    # Update label
    sed -i 's/android:label="flutter_hbb"/android:label="'"$APP_NAME"'"/' "$MANIFEST_FILE"

    # Update intent filter scheme
    sed -i 's/flutter_hbb/opendeskviewer/g' "$MANIFEST_FILE"

    echo "✓ AndroidManifest.xml updated"
else
    echo "ℹ AndroidManifest.xml not found"
fi

# 3. Rename Kotlin package directory
echo "Renaming Kotlin package directory..."
if [ -d "app/src/main/kotlin/com/carriez/flutter_hbb" ]; then
    mkdir -p "app/src/main/kotlin/$PACKAGE_NAME"
    mv "app/src/main/kotlin/com/carriez/flutter_hbb"/* "app/src/main/kotlin/$PACKAGE_NAME/" 2>/dev/null || true
    rm -rf "app/src/main/kotlin/com/carriez"
    echo "✓ Kotlin package renamed"
else
    echo "ℹ No Kotlin package directory to rename"
fi

# 4. Update pubspec.yaml name
echo "Updating pubspec.yaml..."
PUBSPEC="$FLUTTER_DIR/pubspec.yaml"
if [ -f "$PUBSPEC" ]; then
    sed -i 's/name: flutter_hbb/name: opendeskviewer/' "$PUBSPEC"
    sed -i 's/description: Your Remote Desktop Software/description: '"$APP_NAME"' Remote Desktop/' "$PUBSPEC"
    echo "✓ pubspec.yaml updated"
fi

# 5. Update drawable icons
echo "Checking launcher icons..."
ICONS_DIR="app/src/main/res"

if [ -d "../android/icons" ]; then
    mkdir -p "$ICONS_DIR/mipmap-mdpi" "$ICONS_DIR/mipmap-hdpi" "$ICONS_DIR/mipmap-xhdpi" "$ICONS_DIR/mipmap-xxhdpi" "$ICONS_DIR/mipmap-xxxhdpi"
    cp "../android/icons/ic_launcher.png" "$ICONS_DIR/mipmap-mdpi/ic_launcher.png" 2>/dev/null || true
    cp "../android/icons/ic_launcher.png" "$ICONS_DIR/mipmap-hdpi/ic_launcher.png" 2>/dev/null || true
    cp "../android/icons/ic_launcher.png" "$ICONS_DIR/mipmap-xhdpi/ic_launcher.png" 2>/dev/null || true
    cp "../android/icons/ic_launcher.png" "$ICONS_DIR/mipmap-xxhdpi/ic_launcher.png" 2>/dev/null || true
    cp "../android/icons/ic_launcher.png" "$ICONS_DIR/mipmap-xxxhdpi/ic_launcher.png" 2>/dev/null || true
    echo "✓ Launcher icons updated"
else
    echo "ℹ Launcher icons not found at ../android/icons (using existing)"
fi

# 6. Update signing configuration
echo "Checking signing configuration..."
if [ -f "$ANDROID_DIR/app/key.properties" ]; then
    echo "✓ Signing configuration found"
else
    echo "ℹ No signing configuration found. Create key.properties in app/ directory"
fi

echo ""
echo "=== Rebranding Complete ==="
echo ""
echo "To build the APK:"
echo "  cd $FLUTTER_DIR"
echo "  flutter pub get"
echo "  flutter build apk --release"
echo ""
