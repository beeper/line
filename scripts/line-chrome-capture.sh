#!/bin/bash
# Launch Chrome with CDP-based network capture for LINE extension analysis.
#
# Usage:
#   ./scripts/line-chrome-capture.sh [output_dir]
#
# This starts Chrome with remote debugging and runs a CDP listener that
# captures all LINE traffic including SSE EventSource message bodies.
#
# Uses a persistent separate Chrome profile at:
#   ${LINE_CAPTURE_CHROME_PROFILE:-$HOME/.cache/line-chrome-extension-capture/chrome-debug-profile}
# On first run, you'll need to log in to LINE. The profile persists across
# capture output directories.
#
# The capture profile is pre-seeded to skip Chrome's first-run prompts: it does
# not make Chrome the default browser, does not send usage/crash reports, and
# stays signed out of Chrome/Sync. LINE login is still manual.
#
# If LINE/Codex Chrome extensions are already installed in your normal Chrome
# Default profile, this script stages clean unpacked copies into:
#   ${LINE_CAPTURE_EXTENSION_CACHE_DIR:-$HOME/.cache/line-chrome-extension-capture/extensions}
# and auto-loads those copies into the capture profile. If LINE is already
# installed in the persistent capture profile, the script uses that installation
# instead of injecting a staged copy. Override detection with:
#   LINE_CHROME_EXTENSION_DIR=/path/to/LINE/3.7.2_0
#   CODEX_CHROME_EXTENSION_DIR=/path/to/Codex/1.1.5_0
# Disable staged extension auto-loading with:
#   LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0
# This is useful when installing LINE from the Chrome Web Store into the
# persistent capture profile.
#
# When done, close Chrome (Cmd+Q). The capture files will be at:
#   <output_dir>/line-cdp-capture.json  — CDP capture (SSE + request bodies)
#   <output_dir>/line-net-log.json      — Chrome net-log (low-level backup)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${1:-/tmp/line-chrome-extension-capture}"
mkdir -p "$OUTPUT_DIR"
NETLOG_FILE="$OUTPUT_DIR/line-net-log.json"
CDP_FILE="$OUTPUT_DIR/line-cdp-capture.json"
DEBUG_PROFILE="${LINE_CAPTURE_CHROME_PROFILE:-$HOME/.cache/line-chrome-extension-capture/chrome-debug-profile}"
EXTENSION_CACHE_DIR="${LINE_CAPTURE_EXTENSION_CACHE_DIR:-$HOME/.cache/line-chrome-extension-capture/extensions}"
AUTO_LOAD_EXTENSIONS="${LINE_CAPTURE_AUTO_LOAD_EXTENSIONS:-1}"

seed_chrome_capture_profile() {
    local profile_dir="$1"
    local default_dir="$profile_dir/Default"

    mkdir -p "$default_dir"
    chmod 700 "$profile_dir" 2>/dev/null || true
    chmod 700 "$default_dir" 2>/dev/null || true

    # Chrome uses this sentinel for first-run completion. The JSON preferences
    # below keep the choices explicit and readable for future maintainers.
    touch "$profile_dir/First Run"

    python3 - "$profile_dir" <<'PY'
import json
import sys
from pathlib import Path

profile = Path(sys.argv[1])

def merge(dst, src):
    for key, value in src.items():
        if isinstance(value, dict) and isinstance(dst.get(key), dict):
            merge(dst[key], value)
        else:
            dst[key] = value
    return dst

def update_json(path, values):
    try:
        data = json.loads(path.read_text()) if path.exists() else {}
        if not isinstance(data, dict):
            data = {}
    except json.JSONDecodeError:
        data = {}
    merge(data, values)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
    path.chmod(0o600)

update_json(profile / "Local State", {
    "browser": {
        "check_default_browser": False,
        "has_seen_welcome_page": True,
    },
    "distribution": {
        "import_bookmarks": False,
        "import_history": False,
        "make_chrome_default": False,
        "skip_first_run_ui": True,
        "suppress_first_run_default_browser_prompt": True,
    },
    "signin": {
        "allowed": False,
        "allowed_on_next_startup": False,
    },
    "sync": {
        "suppress_start": True,
    },
    "user_experience_metrics": {
        "reporting_enabled": False,
    },
})

update_json(profile / "Default" / "Preferences", {
    "browser": {
        "check_default_browser": False,
        "has_seen_welcome_page": True,
    },
    "credentials_enable_autosignin": False,
    "credentials_enable_service": False,
    "distribution": {
        "import_bookmarks": False,
        "import_history": False,
        "make_chrome_default": False,
        "skip_first_run_ui": True,
        "suppress_first_run_default_browser_prompt": True,
    },
    "profile": {
        "exit_type": "Normal",
        "exited_cleanly": True,
        "name": "LINE Capture",
    },
    "signin": {
        "allowed": False,
        "allowed_on_next_startup": False,
    },
    "sync": {
        "requested": False,
        "suppress_start": True,
    },
})
PY
}

find_latest_extension_dir() {
    local extension_id="$1"
    local base="$HOME/Library/Application Support/Google/Chrome/Default/Extensions/$extension_id"
    if [ -d "$base" ]; then
        find "$base" -mindepth 1 -maxdepth 1 -type d | sort -V | tail -n 1
    fi
}

find_latest_profile_extension_dir() {
    local profile_dir="$1"
    local extension_id="$2"
    local base="$profile_dir/Default/Extensions/$extension_id"
    if [ -d "$base" ]; then
        find "$base" -mindepth 1 -maxdepth 1 -type d | sort -V | tail -n 1
    fi
}

read_manifest_version() {
    local extension_dir="$1"
    if [ -n "${extension_dir:-}" ] && [ -f "$extension_dir/manifest.json" ]; then
        python3 - "$extension_dir/manifest.json" <<'PY'
import json
import sys
from pathlib import Path

try:
    print(json.loads(Path(sys.argv[1]).read_text()).get("version", ""))
except Exception:
    print("")
PY
    fi
}

find_repo_extension_version() {
    local client_file="$SCRIPT_DIR/../pkg/line/client.go"
    if [ -f "$client_file" ]; then
        python3 - "$client_file" <<'PY'
import re
import sys
from pathlib import Path

match = re.search(r'ExtensionVersion\s*=\s*"([^"]+)"', Path(sys.argv[1]).read_text())
print(match.group(1) if match else "")
PY
    fi
}

stage_extension_dir() {
    local extension_id="$1"
    local source_dir="$2"

    if [ -z "${source_dir:-}" ] || [ ! -f "$source_dir/manifest.json" ]; then
        return 0
    fi

    local version
    version="$(python3 - "$source_dir/manifest.json" <<'PY'
import json
import sys
from pathlib import Path

try:
    version = json.loads(Path(sys.argv[1]).read_text()).get("version", "unknown")
except Exception:
    version = "unknown"
print("".join(c if c.isalnum() or c in ".-_" else "_" for c in str(version)))
PY
)"

    local dest="$EXTENSION_CACHE_DIR/$extension_id/$version"
    mkdir -p "$dest"

    # Chrome Web Store extension directories contain _metadata/, which Chrome
    # rejects when loading as unpacked. Stage a clean copy for capture runs.
    if command -v rsync >/dev/null 2>&1; then
        rsync -a --delete --exclude '_metadata' "$source_dir/" "$dest/"
    else
        local tmp="$dest.tmp"
        rm -rf "$tmp"
        mkdir -p "$tmp"
        (cd "$source_dir" && tar --exclude './_metadata' -cf - .) | (cd "$tmp" && tar -xf -)
        rm -rf "$dest"
        mv "$tmp" "$dest"
    fi
    chmod -R go-rwx "$EXTENSION_CACHE_DIR" 2>/dev/null || true

    echo "$dest"
}

LINE_EXTENSION_DIR=""
CODEX_EXTENSION_DIR=""
LINE_PROFILE_EXTENSION_DIR="$(find_latest_profile_extension_dir "$DEBUG_PROFILE" ophjlpahpchlmihnnnihgmmeilfjmjjc)"
LINE_PROFILE_EXTENSION_VERSION="$(read_manifest_version "$LINE_PROFILE_EXTENSION_DIR")"
if [ "$AUTO_LOAD_EXTENSIONS" != "0" ]; then
    if [ -z "${LINE_PROFILE_EXTENSION_DIR:-}" ]; then
        LINE_EXTENSION_SOURCE_DIR="${LINE_CHROME_EXTENSION_DIR:-$(find_latest_extension_dir ophjlpahpchlmihnnnihgmmeilfjmjjc)}"
        LINE_EXTENSION_DIR="$(stage_extension_dir ophjlpahpchlmihnnnihgmmeilfjmjjc "$LINE_EXTENSION_SOURCE_DIR")"
    fi
    CODEX_EXTENSION_SOURCE_DIR="${CODEX_CHROME_EXTENSION_DIR:-$(find_latest_extension_dir hehggadaopoacecdllhhajmbjkdcmajg)}"
    CODEX_EXTENSION_DIR="$(stage_extension_dir hehggadaopoacecdllhhajmbjkdcmajg "$CODEX_EXTENSION_SOURCE_DIR")"
fi
LINE_STAGED_EXTENSION_VERSION="$(read_manifest_version "$LINE_EXTENSION_DIR")"
LINE_CHROME_EXTENSION_VERSION="${LINE_PROFILE_EXTENSION_VERSION:-$LINE_STAGED_EXTENSION_VERSION}"
REPO_EXTENSION_VERSION="$(find_repo_extension_version)"

EXTENSION_DIRS=()
if [ -n "${LINE_EXTENSION_DIR:-}" ] && [ -f "$LINE_EXTENSION_DIR/manifest.json" ]; then
    EXTENSION_DIRS+=("$LINE_EXTENSION_DIR")
fi
if [ -n "${CODEX_EXTENSION_DIR:-}" ] && [ -f "$CODEX_EXTENSION_DIR/manifest.json" ]; then
    EXTENSION_DIRS+=("$CODEX_EXTENSION_DIR")
fi

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
if [ ! -x "$CHROME" ]; then
    echo "Chrome not found at $CHROME"
    echo "Set CHROME env var to your Chrome binary path."
    exit 1
fi

FIRST_RUN=false
if [ ! -d "$DEBUG_PROFILE" ]; then
    FIRST_RUN=true
    mkdir -p "$DEBUG_PROFILE"
fi
seed_chrome_capture_profile "$DEBUG_PROFILE"

echo "==> Starting Chrome with CDP + net-log capture"
echo "    CDP output:  $CDP_FILE"
echo "    Net-log:     $NETLOG_FILE"
echo "    Profile:     $DEBUG_PROFILE"
if [ -n "${LINE_PROFILE_EXTENSION_DIR:-}" ]; then
    echo "    LINE profile extension: $LINE_PROFILE_EXTENSION_DIR"
fi
if [ -n "${REPO_EXTENSION_VERSION:-}" ]; then
    echo "    Repo LINE version: $REPO_EXTENSION_VERSION"
fi
if [ -n "${LINE_CHROME_EXTENSION_VERSION:-}" ]; then
    echo "    Chrome LINE version: $LINE_CHROME_EXTENSION_VERSION"
fi
if [ -n "${REPO_EXTENSION_VERSION:-}" ] && [ -n "${LINE_CHROME_EXTENSION_VERSION:-}" ] && [ "$REPO_EXTENSION_VERSION" != "$LINE_CHROME_EXTENSION_VERSION" ]; then
    echo "    WARNING: repo ExtensionVersion ($REPO_EXTENSION_VERSION) does not match Chrome extension ($LINE_CHROME_EXTENSION_VERSION)."
fi
if [ ${#EXTENSION_DIRS[@]} -gt 0 ]; then
    echo "    Extensions:"
    for ext_dir in "${EXTENSION_DIRS[@]}"; do
        echo "      - $ext_dir"
    done
else
    echo "    Extensions:  none auto-loaded"
fi
echo ""
if [ "$FIRST_RUN" = true ]; then
    echo "    FIRST RUN: Chrome first-run/sign-in prompts are pre-skipped."
    echo "    Log in to LINE manually in the capture browser."
    if [ ${#EXTENSION_DIRS[@]} -eq 0 ]; then
        echo "    LINE extension was not auto-detected; install it from the Chrome Web Store first."
    fi
    echo "    The profile persists across capture dirs — you only need to do this once."
    echo ""
fi
echo "    1. Open the LINE Chrome Extension and do your flow"
echo "    2. When done, close Chrome (Cmd+Q)"
echo ""

# Chrome 146+ requires --user-data-dir for remote debugging.
CHROME_ARGS=(
    --no-first-run
    --no-default-browser-check
    --disable-sync
    --disable-features=SignInPromo,SigninInterception,ChromeSigninIntercept,DiceWebSigninInterception
    --remote-debugging-port=9222
    --remote-allow-origins="*"
    --user-data-dir="$DEBUG_PROFILE"
    --log-net-log="$NETLOG_FILE"
    --net-log-capture-mode=IncludeSensitive
)

if [ ${#EXTENSION_DIRS[@]} -gt 0 ]; then
    IFS=,
    EXTENSION_ARG="${EXTENSION_DIRS[*]}"
    unset IFS
    if [ -z "${LINE_PROFILE_EXTENSION_DIR:-}" ]; then
        CHROME_ARGS+=(--disable-extensions-except="$EXTENSION_ARG")
    fi
    CHROME_ARGS+=(--load-extension="$EXTENSION_ARG")
fi

START_URL="${LINE_CAPTURE_START_URL:-}"
if [ -z "$START_URL" ] && [ -n "${LINE_PROFILE_EXTENSION_DIR:-}" ] && [ -f "$LINE_PROFILE_EXTENSION_DIR/manifest.json" ]; then
    START_URL="chrome-extension://ophjlpahpchlmihnnnihgmmeilfjmjjc/index.html"
fi
if [ -z "$START_URL" ] && [ -n "${LINE_EXTENSION_DIR:-}" ] && [ -f "$LINE_EXTENSION_DIR/manifest.json" ]; then
    START_URL="chrome-extension://ophjlpahpchlmihnnnihgmmeilfjmjjc/index.html"
fi
if [ -z "$START_URL" ]; then
    START_URL="about:blank"
fi

"$CHROME" "${CHROME_ARGS[@]}" "$START_URL" 2>/dev/null &
CHROME_PID=$!

# Start the CDP capture (it waits for Chrome to be ready)
python3 "$SCRIPT_DIR/line-cdp-capture.py" "$CDP_FILE" &
CDP_PID=$!

# Wait for Chrome to exit
wait $CHROME_PID 2>/dev/null || true

# Give CDP listener a moment to finish saving
sleep 1

# Stop CDP capture if still running
if kill -0 $CDP_PID 2>/dev/null; then
    kill $CDP_PID 2>/dev/null
    wait $CDP_PID 2>/dev/null || true
fi

echo ""
echo "==> Chrome closed. Captures saved:"
[ -f "$CDP_FILE" ] && echo "    CDP:     $CDP_FILE ($(du -h "$CDP_FILE" | cut -f1))"
[ -f "$NETLOG_FILE" ] && echo "    Net-log: $NETLOG_FILE ($(du -h "$NETLOG_FILE" | cut -f1))"
