#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

AGYP_HOME_DIR="${AGYP_HOME:-$HOME/.agyp}"
LOCAL_BIN_DIR="$HOME/.local/bin"

mkdir -p "$LOCAL_BIN_DIR" "$AGYP_HOME_DIR/tools/tests" "$AGYP_HOME_DIR/reports"

if [[ -f "$ROOT_DIR/bin/agyp" ]]; then
  cp -f "$ROOT_DIR/bin/agyp" "$LOCAL_BIN_DIR/agyp"
elif [[ -f "$ROOT_DIR/agyp" ]]; then
  cp -f "$ROOT_DIR/agyp" "$LOCAL_BIN_DIR/agyp"
else
  echo "Error: cannot find agyp executable in $ROOT_DIR/bin/agyp" >&2
  exit 1
fi
chmod 755 "$LOCAL_BIN_DIR/agyp"

if [[ -f "$ROOT_DIR/ensure-gateway.py" ]]; then
  cp -f "$ROOT_DIR/ensure-gateway.py" "$AGYP_HOME_DIR/ensure-gateway.py"
  chmod 755 "$AGYP_HOME_DIR/ensure-gateway.py"
fi

if [[ -f "$ROOT_DIR/tools/agyp_intelligence_check.py" ]]; then
  cp -f "$ROOT_DIR/tools/agyp_intelligence_check.py" "$AGYP_HOME_DIR/tools/agyp_intelligence_check.py"
  chmod 755 "$AGYP_HOME_DIR/tools/agyp_intelligence_check.py"
fi

if [[ -f "$ROOT_DIR/tools/tests/agyp_gateway_hardening_test.py" ]]; then
  cp -f "$ROOT_DIR/tools/tests/agyp_gateway_hardening_test.py" "$AGYP_HOME_DIR/tools/tests/agyp_gateway_hardening_test.py"
fi

if [[ ! -f "$AGYP_HOME_DIR/profiles.json" ]]; then
  umask 077
  printf '{\n  "active_profile": "",\n  "profiles": {}\n}\n' > "$AGYP_HOME_DIR/profiles.json"
  chmod 600 "$AGYP_HOME_DIR/profiles.json"
fi

if command -v go >/dev/null 2>&1 && [[ -d "$ROOT_DIR/desktop" ]]; then
  echo "[install] Building desktop monitor..."
  mkdir -p "$ROOT_DIR/desktop/bin"
  if [[ "$(uname -s)" == "Darwin" ]] && [[ -f "$ROOT_DIR/scripts/package-macos.sh" ]]; then
    if bash "$ROOT_DIR/scripts/package-macos.sh" --arch native --out-dir "$ROOT_DIR/desktop/bin"; then
      if [[ "$ROOT_DIR" != "$AGYP_HOME_DIR" ]]; then
        mkdir -p "$AGYP_HOME_DIR/desktop/bin"
        cp -f "$ROOT_DIR/desktop/bin/agyp-desktop" "$AGYP_HOME_DIR/desktop/bin/agyp-desktop"
        rm -rf "$AGYP_HOME_DIR/desktop/bin/agyp Monitor.app"
        cp -R "$ROOT_DIR/desktop/bin/agyp Monitor.app" "$AGYP_HOME_DIR/desktop/bin/agyp Monitor.app"
      fi
    else
      echo "[install] Warning: desktop build failed (requires Go 1.27+); CLI installed cleanly."
      echo "          You can download prebuilt agyp Monitor.app from GitHub Releases."
    fi
  else
    if (cd "$ROOT_DIR/desktop" && go build -o bin/agyp-desktop .); then
      if [[ "$ROOT_DIR" != "$AGYP_HOME_DIR" ]]; then
        mkdir -p "$AGYP_HOME_DIR/desktop/bin"
        cp -f "$ROOT_DIR/desktop/bin/agyp-desktop" "$AGYP_HOME_DIR/desktop/bin/agyp-desktop"
      fi
    else
      echo "[install] Warning: desktop build failed (requires Go 1.27+); CLI installed cleanly."
    fi
  fi
else
  echo "[install] Note: Go compiler not found; skipping desktop source build."
  echo "          CLI is ready; on macOS you can download agyp Monitor.app from GitHub Releases."
fi

if [[ "${1:-}" == "--install-launchagent" ]] && [[ "$(uname -s)" == "Darwin" ]]; then
  PLIST_PATH="$HOME/Library/LaunchAgents/io.agyp.gateway.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.agyp.gateway</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/python3</string>
    <string>$AGYP_HOME_DIR/ensure-gateway.py</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/bin:/bin:/usr/sbin:/sbin:$LOCAL_BIN_DIR</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>StartInterval</key>
  <integer>300</integer>
  <key>StandardOutPath</key>
  <string>$AGYP_HOME_DIR/launchd.out.log</string>
  <key>StandardErrorPath</key>
  <string>$AGYP_HOME_DIR/launchd.err.log</string>
</dict>
</plist>
EOF
  launchctl bootout "gui/$(id -u)/io.agyp.gateway" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
  echo "[install] LaunchAgent installed: $PLIST_PATH"
fi

echo "Done! Next steps:"
echo "  1. Ensure $LOCAL_BIN_DIR is in your PATH"
echo "  2. Configure OAuth or import from macOS Keychain:"
echo "     agyp add work --from-keychain"
echo "     # or: agyp auth config --client-id ... --client-secret ... && agyp add work"
