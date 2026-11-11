#!/usr/bin/env python3
"""Find text, PII and faces in raw screenshots and write evidence/boxes/<image>.json.

Usage:
  detect.py --feature test/<slug> [--plan PLAN.md] [--allow TEXT ...] [--engine auto|vision|tesseract] IMAGE...

IMAGE is a file in evidence/raw/ (name or path). Everything runs on the device:
Apple Vision on macOS (a small Swift program, compiled once into a cache
folder), tesseract elsewhere. Faces come from Vision, or OpenCV when it is
installed; otherwise the script says faces were not checked.

Each OCR word is hidden unless it is part of a PLAN.md `## Test data` string,
an --allow string, or the built-in generic UI list. Phone numbers, emails and
LINE MIDs are always hidden unless the match is itself test data. Hidden
words next to each other on a line become one box.

A re-run replaces only the "auto" and "kept" lists. "add", "hide" and
"unhide" (your edits) stay, and box IDs are stable, so the result repeats.
The JSON never stores hidden text, only its kind and length.
"""

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import unicodedata

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import manifest  # noqa: E402
from evidence_lib import PII, SAFE_WORD, Feature, die, load_json, norm, parse_plan, save_json, ui_phrases, words  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))


def cache_dir():
    base = os.environ.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")
    return os.path.join(base, "line-evidence")


def vision_binary():
    """Compile vision-detect.swift once per source version; None when not on macOS."""
    if sys.platform != "darwin" or not shutil.which("swiftc"):
        return None
    src = os.path.join(HERE, "vision-detect.swift")
    tag = hashlib.sha256(open(src, "rb").read()).hexdigest()[:12]
    out = os.path.join(cache_dir(), f"vision-detect-{tag}")
    if not os.path.exists(out):
        os.makedirs(cache_dir(), exist_ok=True)
        r = subprocess.run(["swiftc", "-O", "-o", out, src], capture_output=True, text=True)
        if r.returncode:
            print(f"warning: cannot compile Vision helper, falling back:\n{r.stderr[-800:]}", file=sys.stderr)
            return None
    return out


def run_vision(binary, image):
    r = subprocess.run([binary, image], capture_output=True, text=True)
    if r.returncode:
        die(f"Vision failed on {image}: {r.stderr.strip()}")
    return json.loads(r.stdout)


def run_tesseract(image):
    from PIL import Image

    langs = subprocess.run(["tesseract", "--list-langs"], capture_output=True, text=True).stdout.split()
    lang = "+".join(l for l in ("eng", "jpn") if l in langs) or "eng"
    r = subprocess.run(["tesseract", image, "-", "-l", lang, "tsv"], capture_output=True, text=True)
    if r.returncode:
        die(f"tesseract failed on {image}: {r.stderr.strip()}")
    out, lines = [], {}
    for row in r.stdout.splitlines()[1:]:
        f = row.split("\t")
        if len(f) < 12 or f[0] != "5" or not f[11].strip() or float(f[10]) < 0:
            continue
        key = (f[1], f[2], f[3], f[4])
        out.append({"text": f[11], "line": lines.setdefault(key, len(lines)), "conf": float(f[10]) / 100,
                    "x": int(f[6]), "y": int(f[7]), "w": int(f[8]), "h": int(f[9])})
    with Image.open(image) as im:
        w, h = im.size
    return {"width": w, "height": h, "words": out, "faces": opencv_faces(image)}


def opencv_faces(image):
    """Faces with OpenCV's bundled Haar cascade, or None when OpenCV is not installed."""
    try:
        import cv2
    except ImportError:
        return None
    img = cv2.imread(image)
    if img is None:
        return []
    cascade = cv2.CascadeClassifier(os.path.join(cv2.data.haarcascades, "haarcascade_frontalface_default.xml"))
    found = cascade.detectMultiScale(cv2.cvtColor(img, cv2.COLOR_BGR2GRAY), 1.1, 5, minSize=(24, 24))
    return [{"x": int(x), "y": int(y), "w": int(w), "h": int(h)} for x, y, w, h in found]


def analyze(image, engine):
    if engine in ("auto", "vision"):
        binary = vision_binary()
        if binary:
            return "vision", run_vision(binary, image)
        if engine == "vision":
            die("Apple Vision is only available on macOS with swiftc (Xcode Command Line Tools)")
    if not shutil.which("tesseract"):
        die("no OCR engine: install tesseract (or use macOS with Vision); run the preflight")
    return "tesseract", run_tesseract(image)


def phrase_spans(tokens, phrases):
    """Indexes of tokens covered by any phrase (a list of normalized word lists)."""
    covered = set()
    for p in phrases:
        n = len(p)
        for i in range(len(tokens) - n + 1):
            if tokens[i:i + n] == p:
                covered.update(range(i, i + n))
    return covered


def box_id(kind, text, b):
    key = f"{kind}|{norm(text)}|{b['x'] // 8}|{b['y'] // 8}|{b['w'] // 8}|{b['h'] // 8}"
    return kind[0] + hashlib.sha1(key.encode()).hexdigest()[:7]


def union(bs):
    x0, y0 = min(b["x"] for b in bs), min(b["y"] for b in bs)
    x1, y1 = max(b["x"] + b["w"] for b in bs), max(b["y"] + b["h"] for b in bs)
    return {"x": x0, "y": y0, "w": x1 - x0, "h": y1 - y0}


def classify(result, test_data, extra):
    allowed = [words(s) for s in test_data + extra]
    allowed = [p for p in allowed if p]
    ui = [words(s) for s in ui_phrases()]
    allowed_whole = {norm(s) for s in test_data + extra}
    auto, kept = [], []

    by_line = {}
    for w in result["words"]:
        by_line.setdefault(w["line"], []).append(w)
    for line in by_line.values():
        line.sort(key=lambda w: w["x"])
        tokens = [norm(w["text"]) for w in line]
        text = " ".join(w["text"] for w in line)
        # Character offsets of each word in `text`, to map PII matches back to words.
        offsets, pos = [], 0
        for w in line:
            offsets.append((pos, pos + len(w["text"])))
            pos += len(w["text"]) + 1
        pii = {}
        for kind, rx in PII:
            for m in rx.finditer(text):
                if norm(m.group()) in allowed_whole:
                    continue
                for i, (s, e) in enumerate(offsets):
                    if s < m.end() and m.start() < e:
                        pii.setdefault(i, kind)
        test = phrase_spans(tokens, allowed)
        generic = phrase_spans(tokens, ui)
        state = []
        for i, w in enumerate(line):
            if i in pii:
                state.append(("hide", pii[i]))
            elif i in test:
                state.append(("keep", "test data"))
            elif i in generic or SAFE_WORD.match(unicodedata.normalize("NFKC", w["text"]).strip()):
                state.append(("keep", "ui"))
            else:
                state.append(("hide", "text"))
        i = 0
        while i < len(line):
            verdict, why = state[i]
            j = i
            while j + 1 < len(line) and state[j + 1] == state[i]:
                j += 1
            group = line[i:j + 1]
            b = union(group)
            joined = " ".join(w["text"] for w in group)
            if verdict == "hide":
                auto.append({"id": box_id(why, joined, b), "kind": why, **b, "chars": len(joined)})
            else:
                kept.append({"id": box_id("keep", joined, b), "kind": why, **b, "text": joined})
            i = j + 1

    for f in result.get("faces") or []:
        auto.append({"id": box_id("face", "", f), "kind": "face", **{k: int(f[k]) for k in "xywh"}})
    return auto, kept


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--feature", required=True)
    ap.add_argument("--plan")
    ap.add_argument("--allow", nargs="*", default=[], help="extra strings that may stay visible")
    ap.add_argument("--engine", default="auto", choices=["auto", "vision", "tesseract"])
    ap.add_argument("images", nargs="+")
    a = ap.parse_args()
    feat = Feature(a.feature, a.plan)
    test_data, _, _ = parse_plan(feat.plan)
    if not test_data and not a.allow:
        print("warning: no `## Test data` in the plan and no --allow: only generic UI text stays visible",
              file=sys.stderr)

    for name in a.images:
        image = feat.raw_image(name)
        engine, result = analyze(image, a.engine)
        path = feat.box_path(image)
        old = load_json(path, {})
        allow = a.allow or old.get("allow", [])
        auto, kept = classify(result, test_data, allow)
        data = {
            "image": feat.rel(image),
            "width": int(result["width"]),
            "height": int(result["height"]),
            "engine": engine,
            "faces_checked": result.get("faces") is not None,
            "allow": allow,
            "auto": sorted(auto, key=lambda b: (b["y"], b["x"])),
            "kept": sorted(kept, key=lambda b: (b["y"], b["x"])),
            "add": old.get("add", []),
            "hide": old.get("hide", []),
            "unhide": old.get("unhide", []),
            "approved": old.get("approved"),
        }
        save_json(path, data)
        # New boxes can void the approval; delete outputs written under it.
        manifest.sweep(feat)
        kinds = {}
        for b in auto:
            kinds[b["kind"]] = kinds.get(b["kind"], 0) + 1
        summary = ", ".join(f"{v} {k}" for k, v in sorted(kinds.items())) or "nothing"
        note = "" if data["faces_checked"] else "  (faces NOT checked: look for faces and avatars by eye)"
        print(f"{os.path.basename(image)}: hide {summary}; keep {len(kept)} [{engine}]{note}")
        print(f"  -> {path}")


if __name__ == "__main__":
    main()
