#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

ARCH="native"
VERSION="1.0.0"
OUT_DIR="$ROOT_DIR/desktop/bin"
MAKE_ZIP=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch)
      ARCH="${2:-}"
      shift 2
      ;;
    --version)
      VERSION="${2:-}"
      shift 2
      ;;
    --out-dir)
      OUT_DIR="${2:-}"
      shift 2
      ;;
    --zip)
      MAKE_ZIP=1
      shift
      ;;
    -h|--help)
      echo "Usage: $0 [--arch native|arm64|amd64|universal] [--version 1.0.0] [--out-dir dir] [--zip]"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

case "$ARCH" in
  native|arm64|amd64|universal) ;;
  *)
    echo "Unsupported --arch: $ARCH (expected native|arm64|amd64|universal)" >&2
    exit 1
    ;;
esac

CLEAN_VER="${VERSION#v}"
[[ -n "$CLEAN_VER" ]] || CLEAN_VER="1.0.0"

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
BIN_OUT="$OUT_DIR/agyp-desktop"
APP_DIR="$OUT_DIR/agyp Monitor.app"

echo "[package-macos] Building desktop binary (arch=$ARCH, version=$CLEAN_VER)..."
if [[ "$ARCH" == "universal" ]]; then
  TMP_ARM="$OUT_DIR/.agyp-desktop-arm64"
  TMP_AMD="$OUT_DIR/.agyp-desktop-amd64"
  (cd "$ROOT_DIR/desktop" && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o "$TMP_ARM" .)
  (cd "$ROOT_DIR/desktop" && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o "$TMP_AMD" .)
  lipo -create -output "$BIN_OUT" "$TMP_ARM" "$TMP_AMD"
  rm -f "$TMP_ARM" "$TMP_AMD"
elif [[ "$ARCH" == "native" ]]; then
  (cd "$ROOT_DIR/desktop" && go build -ldflags="-s -w" -o "$BIN_OUT" .)
else
  (cd "$ROOT_DIR/desktop" && GOOS=darwin GOARCH="$ARCH" go build -ldflags="-s -w" -o "$BIN_OUT" .)
fi
chmod 755 "$BIN_OUT"

echo "[package-macos] Assembling $APP_DIR..."
rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"
cp -f "$BIN_OUT" "$APP_DIR/Contents/MacOS/agyp-desktop"
chmod 755 "$APP_DIR/Contents/MacOS/agyp-desktop"

if [[ -f "$ROOT_DIR/desktop/AppIcon.icns" ]]; then
  cp -f "$ROOT_DIR/desktop/AppIcon.icns" "$APP_DIR/Contents/Resources/AppIcon.icns"
fi

cat > "$APP_DIR/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>agyp-desktop</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundleIdentifier</key>
    <string>com.agyp.monitor</string>
    <key>CFBundleName</key>
    <string>agyp Monitor</string>
    <key>CFBundleDisplayName</key>
    <string>agyp Monitor</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${CLEAN_VER}</string>
    <key>CFBundleVersion</key>
    <string>${CLEAN_VER}</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "$APP_DIR"
fi

if [[ "$MAKE_ZIP" -eq 1 ]]; then
  ZIP_NAME="agyp-Monitor-macos-${ARCH}.zip"
  ZIP_PATH="$OUT_DIR/$ZIP_NAME"
  rm -f "$ZIP_PATH" "$OUT_DIR/SHA256SUMS.txt"
  ditto -c -k --sequesterRsrc --keepParent "$APP_DIR" "$ZIP_PATH"
  (cd "$OUT_DIR" && shasum -a 256 "$ZIP_NAME" > SHA256SUMS.txt)
  echo "[package-macos] Created $ZIP_PATH and SHA256SUMS.txt"
fi
