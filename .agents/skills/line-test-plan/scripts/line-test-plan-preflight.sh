#!/bin/bash
# Check what make-samples.py needs to generate labeled sample media for a test plan.
# Only needed when the feature sends or receives media. Works on macOS, Linux and
# Windows (Git Bash / MSYS2).
#
# Usage:
#   .agents/skills/line-test-plan/scripts/line-test-plan-preflight.sh             # human/agent-readable report
#   eval "$(.agents/skills/line-test-plan/scripts/line-test-plan-preflight.sh --print-env)"   # sets the PYTHON array
#
# The report prints the detected OS, one OK line per dependency found, one
# MISSING line per required dependency that is not, and one OPTIONAL line per
# optional one. It deliberately does not say how to install anything: the agent
# running it picks the install method that fits the OS and the package managers
# available, and asks the user first.
# Exits 1 when a required dependency is missing. Optional ones never fail it;
# make-samples.py skips the formats it cannot encode and says which.
#
# Override detection with PYTHON=/path/to/python (one executable; spaces are fine).

set -uo pipefail

case "$(uname -s)" in
    Darwin) OS=macos ;;
    Linux)
        if grep -qi microsoft /proc/version 2>/dev/null; then OS=wsl; else OS=linux; fi ;;
    MINGW* | MSYS* | CYGWIN*) OS=windows ;;
    *) OS=unknown ;;
esac

# Sets the PYTHON_FOUND array: the executable first, then optional arguments
# ("py -3"). An array keeps a PYTHON override with spaces in its path in one piece.
find_python() {
    PYTHON_FOUND=()
    local candidate
    if [ -n "${PYTHON:-}" ]; then
        "$PYTHON" -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null && PYTHON_FOUND=("$PYTHON")
        return
    fi
    # "python3" on Windows can be the Microsoft Store stub, so the version check
    # also filters out commands that exist but don't run.
    for candidate in "python3" "python" "py -3"; do
        # shellcheck disable=SC2206  # intentional split of "py -3"
        local cmd=($candidate)
        if "${cmd[@]}" -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null; then
            PYTHON_FOUND=("${cmd[@]}")
            return
        fi
    done
}

find_python

if [ "${1:-}" = "--print-env" ]; then
    printf 'PYTHON=('
    [ ${#PYTHON_FOUND[@]} -gt 0 ] && printf '%q ' "${PYTHON_FOUND[@]}"
    printf ')\n'
    exit 0
fi

missing=0
ok() { echo "OK       $1"; }
need() { echo "MISSING  $1"; missing=1; }
optional() { echo "OPTIONAL $1"; }

echo "OS       $OS ($(uname -sm))"

if [ ${#PYTHON_FOUND[@]} -gt 0 ]; then
    ok "Python 3.8+: ${PYTHON_FOUND[*]} ($("${PYTHON_FOUND[@]}" --version 2>&1))"
    if "${PYTHON_FOUND[@]}" -c "import PIL" >/dev/null 2>&1; then
        ok "Python package Pillow (images: JPEG, PNG, BMP, TIFF, GIF, WebP, APNG)"
        if "${PYTHON_FOUND[@]}" -c "import pillow_heif" >/dev/null 2>&1; then
            ok "Python package pillow-heif (HEIC/HEIF, AVIF)"
        else
            optional "Python package pillow-heif (pip name: pillow-heif): HEIC and AVIF samples, if ffmpeg cannot do them"
        fi
    else
        need "Python package Pillow (pip name: Pillow), for ${PYTHON_FOUND[*]}"
    fi
else
    need "Python 3.8+"
fi

if command -v ffmpeg >/dev/null 2>&1; then
    ok "ffmpeg: $(command -v ffmpeg)"
    enc="$(ffmpeg -hide_banner -encoders 2>/dev/null)"
    for e in libx264 libx265 libvpx-vp9 libaom-av1 libsvtav1; do
        if grep -q " $e " <<<"$enc"; then ok "ffmpeg encoder $e"; else optional "ffmpeg encoder $e (build of ffmpeg without it)"; fi
    done
else
    optional "ffmpeg: video samples (MP4/HEVC/MOV/WebM) and, without pillow-heif, AVIF are skipped"
fi

exit $missing
