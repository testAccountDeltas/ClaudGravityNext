#!/usr/bin/env bash
# ClaudGravityNext client installer for macOS/Linux.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.sh | bash
#
# Non-interactive:
#   CLAUDGRAVITY_URL="1.2.3.4:8898" CLAUDGRAVITY_KEY="sk-..." \
#     curl -fsSL https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.sh | bash
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/testAccountDeltas/ClaudGravityNext/main/install.sh | bash -s -- --uninstall

set -euo pipefail

INSTALL_DIR="$HOME/.claudgravitynext"
OS="$(uname -s)"

say()  { printf "\033[36m==> %s\033[0m\n" "$1"; }
ok()   { printf "    \033[32m%s\033[0m\n" "$1"; }
warn() { printf "    \033[33m%s\033[0m\n" "$1"; }

if [ "${1:-}" = "--uninstall" ]; then
  say "Removing ClaudGravityNext..."
  rm -rf "$INSTALL_DIR"
  if [ -f "$HOME/.local/share/nautilus/scripts/Open Claude Code here" ]; then
    rm -f "$HOME/.local/share/nautilus/scripts/Open Claude Code here"
  fi
  rm -f "$HOME/Desktop/ClaudGravityNext.command" 2>/dev/null || true
  for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
    [ -f "$rc" ] && sed -i.bak "/# >>> claudgravity >>>/,/# <<< claudgravity <<</d" "$rc" 2>/dev/null || true
  done
  ok "Done."
  exit 0
fi

echo ""
echo "  ClaudGravityNext client installer"
echo "  Claude Code <-> ClaudGravityNext gateway client setup"
echo ""

# --- 1. gateway url + key ---
say "Gateway settings"

GATEWAY_URL="${CLAUDGRAVITY_URL:-}"
if [ -z "$GATEWAY_URL" ]; then
  read -rp "Gateway address (e.g. 203.0.113.10 or 203.0.113.10:8898): " GATEWAY_URL
fi
GATEWAY_URL="${GATEWAY_URL%/}"
GATEWAY_URL="${GATEWAY_URL#http://}"
GATEWAY_URL="${GATEWAY_URL#https://}"

API_KEY="${CLAUDGRAVITY_KEY:-}"
if [ -z "$API_KEY" ]; then
  read -rp "API key (sk-...): " API_KEY
fi

if [[ "$GATEWAY_URL" != *:* ]]; then
  GATEWAY_URL="$GATEWAY_URL:8898"
fi
GATEWAY_HOST="${GATEWAY_URL%%:*}"
GATEWAY_PORT="${GATEWAY_URL##*:}"
BASE_URL="http://$GATEWAY_HOST:$GATEWAY_PORT"

# Plain HTTP to the gateway's API port: no CONNECT proxy and no CA are needed.
# (HTTPS_PROXY / SSL_CERT_FILE are left empty in the launcher on purpose.)
say "Checking the gateway ($BASE_URL)..."
CODE="$(curl -s -o /dev/null -m 10 -w '%{http_code}' -H "x-api-key: $API_KEY" "$BASE_URL/v1/models" || true)"
case "$CODE" in
  200) ok "OK - gateway reachable, key accepted" ;;
  401) echo "The gateway rejected the API key (HTTP 401)." >&2; exit 1 ;;
  *)   echo "Could not reach the gateway at $BASE_URL (HTTP ${CODE:-none}). Check the address, port and firewall." >&2; exit 1 ;;
esac

# --- 2. save local files ---
say "Saving configuration to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
cat > "$INSTALL_DIR/config.json" <<EOF2
{
  "base_url": "$BASE_URL",
  "api_key": "$API_KEY"
}
EOF2
ok "config.json saved"

# --- 3. find claude ---
# Order: VS Code extension -> PATH/environment -> native install paths.
say "Looking for a Claude Code install..."
CLAUDE_CMD=""
FOUND_VIA=""

find_in_vscode() {
  local ext_glob
  if [ "$OS" = "Darwin" ]; then
    ext_glob="$HOME/.vscode/extensions/anthropic.claude-code-*-darwin-"*
  else
    ext_glob="$HOME/.vscode/extensions/anthropic.claude-code-*-linux-"*
  fi
  for d in $ext_glob; do
    [ -d "$d" ] || continue
    for bin in "$d/resources/native-binary/claude" "$d/resources/native-binary/claude-code"; do
      [ -f "$bin" ] && { echo "$bin"; return 0; }
    done
  done
  return 1
}

find_in_path() {
  command -v claude 2>/dev/null && return 0
  if [ -n "${CLAUDGRAVITY_CLAUDE_PATH:-}" ] && [ -x "${CLAUDGRAVITY_CLAUDE_PATH:-}" ]; then
    echo "$CLAUDGRAVITY_CLAUDE_PATH"; return 0
  fi
  return 1
}

find_native() {
  for c in \
    "$HOME/.local/bin/claude" \
    "/usr/local/bin/claude" \
    "/opt/homebrew/bin/claude"
  do
    [ -x "$c" ] && { echo "$c"; return 0; }
  done
  if command -v npm >/dev/null 2>&1; then
    local npm_bin
    npm_bin="$(npm bin -g 2>/dev/null || true)"
    if [ -n "$npm_bin" ] && [ -x "$npm_bin/claude" ]; then
      echo "$npm_bin/claude"; return 0
    fi
  fi
  return 1
}

if out="$(find_in_vscode)"; then CLAUDE_CMD="$out"; FOUND_VIA="VS Code extension"; fi
if [ -z "$CLAUDE_CMD" ] && out="$(find_in_path)"; then CLAUDE_CMD="$out"; FOUND_VIA="PATH/environment"; fi
if [ -z "$CLAUDE_CMD" ] && out="$(find_native)"; then CLAUDE_CMD="$out"; FOUND_VIA="native install"; fi

if [ -n "$CLAUDE_CMD" ]; then
  ok "Found ($FOUND_VIA): $CLAUDE_CMD"
else
  warn "Claude Code was not found automatically. launch.sh will use 'claude' from PATH."
  CLAUDE_CMD="claude"
  FOUND_VIA="not found automatically, using 'claude' from PATH"
fi

# --- 4. write launch.sh ---
say "Writing launch.sh"
LAUNCH_PATH="$INSTALL_DIR/launch.sh"
cat > "$LAUNCH_PATH" <<EOF
#!/usr/bin/env bash
export CLAUDE_CONFIG_DIR="$INSTALL_DIR/profile"
export ANTHROPIC_BASE_URL="$BASE_URL"
# set to empty on purpose (not unset), same as the Windows launcher
export HTTP_PROXY=""
export HTTPS_PROXY=""
export ALL_PROXY=""
export ANTHROPIC_AUTH_TOKEN=""
export ANTHROPIC_API_KEY=""
export CLAUDE_CODE_SIMPLE=""
export CLAUDE_CODE_OAUTH_TOKEN="$API_KEY"
export CLAUDE_CODE_OAUTH_SCOPES="user:inference user:profile"
export CLAUDE_CODE_SUBSCRIPTION_TYPE="max"
export CLAUDE_CODE_RATE_LIMIT_TIER="default"
export CLAUDE_CODE_ENABLE_TODO_TOOLS=1
# CLAUDE_CODE_USE_POWERSHELL_TOOL is Windows-only (install.ps1); Linux/macOS use the Bash tool.
export CLAUDE_CODE_ATTRIBUTION_HEADER=0
export CLAUDE_CODE_EFFORT_LEVEL=xhigh
export CLAUDE_CODE_GATEWAY_HINT_HEADERS=1
export _CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1
export DISABLE_AUTOUPDATER=1
exec "$CLAUDE_CMD" "\$@"
EOF
chmod +x "$LAUNCH_PATH"
ok "launch.sh written"

# --- 5. shell integration (claudgravity command) ---
say "Adding the 'claudgravity' command to your shell"
BLOCK=$'\n# >>> claudgravity >>>\nclaudgravity() { "'"$LAUNCH_PATH"'" "$@"; }\n# <<< claudgravity <<<\n'
for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
  if [ -f "$rc" ]; then
    if ! grep -q "# >>> claudgravity >>>" "$rc"; then
      printf '%s' "$BLOCK" >> "$rc"
      ok "Added to $rc"
    fi
  fi
done

# --- 6. OS-specific "open here" integration ---
if [ "$OS" = "Linux" ]; then
  say "Installing a Nautilus script (right click -> Scripts)"
  NAUT_DIR="$HOME/.local/share/nautilus/scripts"
  mkdir -p "$NAUT_DIR"
  NAUT_SCRIPT="$NAUT_DIR/Open Claude Code here"
  cat > "$NAUT_SCRIPT" <<EOF
#!/usr/bin/env bash
cd "\$NAUTILUS_SCRIPT_CURRENT_URI" 2>/dev/null || cd "\$PWD"
x-terminal-emulator -e "$LAUNCH_PATH" 2>/dev/null || gnome-terminal -- "$LAUNCH_PATH" 2>/dev/null || "$LAUNCH_PATH"
EOF
  chmod +x "$NAUT_SCRIPT"
  ok "Done (if your file manager is not Nautilus, run 'claudgravity' in a terminal from the folder you want)"
elif [ "$OS" = "Darwin" ]; then
  say "Creating ClaudGravityNext.command on the Desktop (you can drag it to the Dock)"
  DESKTOP_CMD="$HOME/Desktop/ClaudGravityNext.command"
  if [ -d "$HOME/Desktop" ]; then
    cat > "$DESKTOP_CMD" <<EOF
#!/usr/bin/env bash
cd "\$HOME"
"$LAUNCH_PATH"
EOF
    chmod +x "$DESKTOP_CMD"
    ok "Created: $DESKTOP_CMD"
  fi
  warn "A Finder context-menu entry needs an Automator Quick Action - run 'claudgravity' in a terminal from the folder you want."
fi

echo ""
echo "  Done!"
echo "  Open a NEW terminal and run: claudgravity"
echo "  (Linux: or right click in Nautilus -> Scripts -> \"Open Claude Code here\")"
echo "  Uninstall: curl -fsSL <this-script-url> | bash -s -- --uninstall"
echo ""
