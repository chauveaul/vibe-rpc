#!/bin/sh
# Build VibeNotch.app (universal arm64 + x86_64, ad-hoc signed).
#
#   macos/VibeNotch/build-app.sh            # -> macos/VibeNotch/build/VibeNotch.app
#   macos/VibeNotch/build-app.sh --install  # also copies it to ~/Applications
#
# Then run `vibe-rpc notch` to launch it.
set -eu

cd "$(dirname "$0")"
VERSION="${VIBE_NOTCH_VERSION:-$(grep -o 'Version = "[^"]*"' ../../internal/paths/paths.go | cut -d'"' -f2)}"
APP="build/VibeNotch.app"

echo "Building VibeNotch ${VERSION}…"
swift build -c release --arch arm64 --arch x86_64
BIN="$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)/VibeNotch"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/VibeNotch"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>VibeNotch</string>
  <key>CFBundleDisplayName</key><string>VibeNotch</string>
  <key>CFBundleIdentifier</key><string>io.github.chauveaul.vibenotch</string>
  <key>CFBundleExecutable</key><string>VibeNotch</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <!-- No Dock icon or app menu: VibeNotch lives in the notch only. -->
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF

codesign --force --sign - "$APP" >/dev/null
echo "Built $(pwd)/$APP"

if [ "${1:-}" = "--install" ]; then
  mkdir -p "$HOME/Applications"
  rm -rf "$HOME/Applications/VibeNotch.app"
  cp -R "$APP" "$HOME/Applications/"
  echo "Installed to ~/Applications/VibeNotch.app. Run: vibe-rpc notch"
fi
