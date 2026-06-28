#!/usr/bin/env bash
# Generates and loads a macOS LaunchAgent that runs the slackrun binary.
#
# Idempotent: re-running unloads the old agent before reloading.
# The slackrun binary path is resolved at script execution time; rebuild and
# re-run this script if you move the binary.

set -euo pipefail

LABEL="${SLACKRUN_LABEL:-com.slackrun.slackrun}"
PLIST_PATH="${HOME}/Library/LaunchAgents/${LABEL}.plist"
LOG_PATH="${HOME}/Library/Logs/slackrun.log"
CONFIG_DIR="${HOME}/.config/slackrun"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SLACKRUN_BIN="${SLACKRUN_BIN:-${REPO_ROOT}/slackrun}"

if [[ ! -x "$SLACKRUN_BIN" ]]; then
  echo "error: slackrun binary not found at $SLACKRUN_BIN" >&2
  echo "build it first: (cd $REPO_ROOT && go build -o slackrun ./cmd/slackrun)" >&2
  exit 1
fi

mkdir -p "$CONFIG_DIR"
if [[ -f "${CONFIG_DIR}/.env" ]]; then
  chmod 600 "${CONFIG_DIR}/.env"
else
  echo "warning: ${CONFIG_DIR}/.env does not exist yet; copy .env.example before starting" >&2
fi

resolve_real() {
  local name="$1"
  local candidate
  candidate="$(command -v "$name" || true)"
  if [[ -z "$candidate" ]]; then
    echo ""
    return
  fi
  if readlink -f / >/dev/null 2>&1; then
    readlink -f "$candidate"
  else
    /usr/bin/perl -MCwd -e 'print Cwd::abs_path($ARGV[0])' "$candidate"
  fi
}

# Resolve real paths for anything the rules might invoke (claude, codex, etc.).
# Add more by editing this list — they all get folded into PATH below.
BIN_NAMES=(claude codex git node python3)

PATH_PARTS=("$(dirname "$SLACKRUN_BIN")")
for n in "${BIN_NAMES[@]}"; do
  p="$(resolve_real "$n")"
  if [[ -n "$p" ]]; then
    PATH_PARTS+=("$(dirname "$p")")
  fi
done
PATH_PARTS+=("/usr/local/bin" "/opt/homebrew/bin" "/usr/bin" "/bin")

# Dedupe while preserving order.
declare -A SEEN_PATH=()
DEDUP_PATH=""
for p in "${PATH_PARTS[@]}"; do
  if [[ -n "$p" && -z "${SEEN_PATH[$p]:-}" ]]; then
    DEDUP_PATH="${DEDUP_PATH}${p}:"
    SEEN_PATH[$p]=1
  fi
done
DEDUP_PATH="${DEDUP_PATH%:}"

mkdir -p "$(dirname "$PLIST_PATH")"

cat >"$PLIST_PATH" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>WorkingDirectory</key>
  <string>${REPO_ROOT}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/caffeinate</string>
    <string>-s</string>
    <string>${SLACKRUN_BIN}</string>
    <string>start</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${DEDUP_PATH}</string>
    <key>HOME</key>
    <string>${HOME}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>${LOG_PATH}</string>
  <key>StandardErrorPath</key>
  <string>${LOG_PATH}</string>
</dict>
</plist>
PLIST

chmod 644 "$PLIST_PATH"

if launchctl print "gui/$(id -u)/${LABEL}" >/dev/null 2>&1; then
  launchctl bootout "gui/$(id -u)/${LABEL}" || true
fi
launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
launchctl kickstart -k "gui/$(id -u)/${LABEL}" || true

echo "Loaded ${LABEL}"
echo "  plist:  ${PLIST_PATH}"
echo "  log:    ${LOG_PATH}"
echo "  config: ${CONFIG_DIR}/"
echo
echo "Inspect with:"
echo "  launchctl print gui/\$(id -u)/${LABEL}"
echo "  tail -f ${LOG_PATH}"
