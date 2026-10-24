#!/bin/bash
# Check that everything the LINE Chrome Extension capture needs is installed.
# Works on macOS, Linux and Windows (Git Bash / MSYS2).
#
# Usage:
#   ./scripts/line-capture-preflight.sh             # human/agent-readable report
#   eval "$(./scripts/line-capture-preflight.sh --print-env)"   # sets CHROME and PYTHON
#
# The report prints the detected OS, one OK line per dependency found and one
# MISSING line per dependency that is not. It deliberately does not say how to
# install anything: the agent running it picks the install method that fits
# the OS and the package managers available, and asks the user first.
# Exits 1 when anything required is missing.
#
# Override detection with CHROME=/path/to/chrome or PYTHON=python-command.

set -uo pipefail

case "$(uname -s)" in
    Darwin) OS=macos ;;
    Linux)
        if grep -qi microsoft /proc/version 2>/dev/null; then OS=wsl; else OS=linux; fi ;;
    MINGW* | MSYS* | CYGWIN*) OS=windows ;;
    *) OS=unknown ;;
esac

find_chrome() {
    if [ -n "${CHROME:-}" ]; then
        [ -x "$CHROME" ] && echo "$CHROME"
        return
    fi
    local candidates=()
    case "$OS" in
        macos)
            candidates=("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
                "$HOME/Applications/Google Chrome.app/Contents/MacOS/Google Chrome") ;;
        windows)
            candidates=("${PROGRAMFILES:-/c/Program Files}/Google/Chrome/Application/chrome.exe"
                "${PROGRAMFILES_X86:-/c/Program Files (x86)}/Google/Chrome/Application/chrome.exe"
                "${LOCALAPPDATA:-$HOME/AppData/Local}/Google/Chrome/Application/chrome.exe") ;;
        *)
            local name
            for name in google-chrome google-chrome-stable chromium chromium-browser; do
                command -v "$name" >/dev/null 2>&1 && { command -v "$name"; return; }
            done ;;
    esac
    local path
    for path in "${candidates[@]}"; do
        [ -x "$path" ] && { echo "$path"; return; }
    done
}

find_python() {
    if [ -n "${PYTHON:-}" ]; then
        $PYTHON -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null && echo "$PYTHON"
        return
    fi
    # "python3" on Windows can be the Microsoft Store stub, so the version check
    # also filters out commands that exist but don't run.
    local cmd
    for cmd in python3 python "py -3"; do
        $cmd -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null && { echo "$cmd"; return; }
    done
}

CHROME_FOUND="$(find_chrome)"
PYTHON_FOUND="$(find_python)"

if [ "${1:-}" = "--print-env" ]; then
    printf 'CHROME=%q\nPYTHON=%q\n' "$CHROME_FOUND" "$PYTHON_FOUND"
    exit 0
fi

missing=0
ok() { echo "OK       $1"; }
need() { echo "MISSING  $1"; missing=1; }

echo "OS       $OS ($(uname -sm))"
if [ "$OS" = wsl ]; then
    echo "NOTE     WSL detected: Chrome must be reachable from WSL (a Linux Chrome in WSL, or set CHROME to the Windows chrome.exe)."
fi

if [ -n "$CHROME_FOUND" ]; then
    ok "Google Chrome: $CHROME_FOUND"
else
    need "Google Chrome (or Chromium on Linux); set CHROME=/path/to/chrome if it is installed somewhere else"
fi

if [ -n "$PYTHON_FOUND" ]; then
    ok "Python 3.8+: $PYTHON_FOUND ($($PYTHON_FOUND --version 2>&1))"
    # line-cdp-capture.py records request bodies and SSE events through CDP.
    if $PYTHON_FOUND -c "import websocket" >/dev/null 2>&1; then
        ok "Python package websocket-client"
    else
        need "Python package websocket-client (pip name: websocket-client), for $PYTHON_FOUND"
    fi
else
    need "Python 3.8+"
fi

exit $missing
