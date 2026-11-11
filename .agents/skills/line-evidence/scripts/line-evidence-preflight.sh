#!/bin/bash
# Check that everything line-evidence needs is installed.
# Works on macOS, Linux and Windows (Git Bash / MSYS2).
#
# Usage: .agents/skills/line-evidence/scripts/line-evidence-preflight.sh
#
# Prints the detected OS, one OK line per dependency found and one MISSING
# line per dependency that is not. OPTIONAL lines are not required but leave
# a gap (for example no face detector). It deliberately does not say how to
# install anything: the agent picks the method that fits the OS and asks the
# user first. Exits 1 when anything required is missing.
#
# Override detection with PYTHON=/path/to/python.

set -uo pipefail

case "$(uname -s)" in
    Darwin) OS=macos ;;
    Linux)
        if grep -qi microsoft /proc/version 2>/dev/null; then OS=wsl; else OS=linux; fi ;;
    MINGW* | MSYS* | CYGWIN*) OS=windows ;;
    *) OS=unknown ;;
esac

PY=()
if [ -n "${PYTHON:-}" ]; then
    "$PYTHON" -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null && PY=("$PYTHON")
else
    for candidate in "python3" "python" "py -3"; do
        # shellcheck disable=SC2206  # intentional split of "py -3"
        cmd=($candidate)
        if "${cmd[@]}" -c "import sys; sys.exit(sys.version_info < (3, 8))" 2>/dev/null; then
            PY=("${cmd[@]}")
            break
        fi
    done
fi

missing=0
ok() { echo "OK       $1"; }
need() { echo "MISSING  $1"; missing=1; }
opt() { echo "OPTIONAL $1"; }
has_py() { [ ${#PY[@]} -gt 0 ] && "${PY[@]}" -c "import $1" >/dev/null 2>&1; }

echo "OS       $OS ($(uname -sm))"

if [ ${#PY[@]} -gt 0 ]; then
    ok "Python 3.8+: ${PY[*]} ($("${PY[@]}" --version 2>&1))"
    if has_py PIL; then ok "Python package Pillow"; else need "Python package Pillow (pip name: Pillow), for ${PY[*]}"; fi
else
    need "Python 3.8+"
fi

# OCR and faces: Apple Vision through swiftc on macOS, tesseract elsewhere.
vision=0
if [ "$OS" = macos ] && command -v swiftc >/dev/null 2>&1; then
    vision=1
    ok "Apple Vision OCR and face detection (swiftc: $(command -v swiftc))"
fi
if command -v tesseract >/dev/null 2>&1; then
    langs=" $(tesseract --list-langs 2>/dev/null | tail -n +2 | tr '\n' ' ') "
    have=""
    for l in eng jpn; do case "$langs" in *" $l "*) have="$have $l" ;; esac; done
    ok "tesseract: $(command -v tesseract) (languages:${have:- none of eng, jpn})"
    case "$langs" in *" eng "*) ;; *) need "tesseract English data (eng)" ;; esac
    case "$langs" in *" jpn "*) ;; *) opt "tesseract Japanese data (jpn): Japanese text is read poorly without it" ;; esac
elif [ $vision = 0 ]; then
    need "an OCR engine: tesseract (or macOS with swiftc from the Xcode Command Line Tools, for Apple Vision)"
fi
if [ $vision = 0 ]; then
    if has_py cv2; then
        ok "Face detection: OpenCV (Python package opencv-python-headless)"
    else
        opt "Face detection: none (Apple Vision is macOS only; Python package opencv-python-headless adds it). Without it, faces and avatars are only found by looking at each image."
    fi
fi

# HEIC from iPhones: pillow-heif anywhere, sips on macOS, heif-convert on Linux.
if has_py pillow_heif; then
    ok "HEIC conversion: pillow-heif"
elif [ "$OS" = macos ] && command -v sips >/dev/null 2>&1; then
    ok "HEIC conversion: sips"
elif command -v heif-convert >/dev/null 2>&1; then
    ok "HEIC conversion: heif-convert"
else
    opt "HEIC conversion: none (Python package pillow-heif, or heif-convert from libheif). Only needed for .heic screenshots."
fi

exit $missing
