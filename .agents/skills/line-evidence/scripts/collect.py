#!/usr/bin/env python3
"""Collect a feature's test screenshots into test/<feature>/evidence/raw/.

Usage:
  collect.py --feature test/<slug> [--since T] [--until T] [--add FILE ...] [--dry-run] PATH...

PATH is a folder (not searched recursively) or an image file. Only images
taken inside the time window are kept: --since/--until accept
"YYYY-MM-DD", "YYYY-MM-DD HH:MM" or ISO 8601. Without --since the window
starts at 00:00 on PLAN.md's date (or the day PLAN.md was last changed) and
runs until now. --add files (screenshots the agent took itself) skip the
window check. An image's time is its EXIF DateTimeOriginal, else its mtime.

Duplicates (same SHA-256, also against earlier runs) are skipped. HEIC/HEIF
becomes PNG, and JPEGs with an EXIF rotation are turned upright and saved as
PNG, so box coordinates always match the pixels. Copies are named
<YYYYmmdd-HHMMSS>[-n].<ext> and listed in raw/index.json with their source.
"""

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
from datetime import datetime

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from evidence_lib import IMAGE_EXTS, Feature, die, load_json, parse_plan, save_json  # noqa: E402

try:
    from PIL import Image, ImageOps
except ImportError:
    die("Pillow is missing; run the preflight")


def parse_time(s):
    for fmt in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%d %H:%M", "%Y-%m-%d"):
        try:
            return datetime.strptime(s, fmt)
        except ValueError:
            pass
    try:
        t = datetime.fromisoformat(s)
        return t.astimezone().replace(tzinfo=None) if t.tzinfo else t
    except ValueError:
        die(f"cannot read time {s!r}")


def taken_at(path):
    """EXIF DateTimeOriginal when the file has one, else the mtime."""
    if os.path.splitext(path)[1].lower() in (".jpg", ".jpeg", ".tif", ".tiff", ".webp", ".png"):
        try:
            with Image.open(path) as im:
                exif = im.getexif()
                raw = exif.get_ifd(0x8769).get(0x9003) or exif.get(0x0132)
                if raw:
                    return datetime.strptime(str(raw).strip("\x00 "), "%Y:%m:%d %H:%M:%S")
        except Exception:
            pass
    return datetime.fromtimestamp(os.path.getmtime(path))


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def heic_to_png(src, dst):
    try:
        import pillow_heif

        pillow_heif.register_heif_opener()
        with Image.open(src) as im:
            ImageOps.exif_transpose(im).save(dst, "PNG")
        return
    except ImportError:
        pass
    if shutil.which("sips"):
        subprocess.run(["sips", "-s", "format", "png", src, "--out", dst], check=True, capture_output=True)
    elif shutil.which("heif-convert"):
        subprocess.run(["heif-convert", src, dst], check=True, capture_output=True)
    else:
        die(f"cannot convert HEIC ({src}): no pillow-heif, sips or heif-convert; run the preflight")


def candidates(paths):
    for p in paths:
        p = os.path.expanduser(p)
        if os.path.isdir(p):
            for name in sorted(os.listdir(p)):
                full = os.path.join(p, name)
                if os.path.isfile(full) and os.path.splitext(name)[1].lower() in IMAGE_EXTS:
                    yield full
        elif os.path.isfile(p):
            yield p
        else:
            print(f"skip: not found: {p}", file=sys.stderr)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--feature", required=True)
    ap.add_argument("--plan")
    ap.add_argument("--since")
    ap.add_argument("--until")
    ap.add_argument("--add", nargs="*", default=[], help="agent screenshots; no time check")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("paths", nargs="*")
    a = ap.parse_args()
    feat = Feature(a.feature, a.plan)

    if a.since:
        since = parse_time(a.since)
    else:
        _, _, date = parse_plan(feat.plan)
        if date:
            since = parse_time(date)
        elif os.path.exists(feat.plan):
            since = datetime.fromtimestamp(os.path.getmtime(feat.plan)).replace(hour=0, minute=0, second=0)
        else:
            die("no --since and no PLAN.md to infer the window from; ask the user for the time window")
    until = parse_time(a.until) if a.until else datetime.now()
    print(f"window: {since:%Y-%m-%d %H:%M} .. {until:%Y-%m-%d %H:%M}")

    index = feat.load_index()
    seen = {e["sha256"] for e in index}
    names = {e["file"] for e in index}
    picked = []
    for src, forced in [(p, False) for p in candidates(a.paths)] + [(p, True) for p in candidates(a.add)]:
        t = taken_at(src)
        if not forced and not (since <= t <= until):
            continue
        digest = sha256(src)
        if digest in seen:
            print(f"dup:  {src}")
            continue
        seen.add(digest)
        picked.append((t, src, digest, forced))

    picked.sort()
    if not a.dry_run and picked:
        feat.make_raw()
    for t, src, digest, forced in picked:
        ext = os.path.splitext(src)[1].lower()
        convert = ext in (".heic", ".heif")
        if not convert and ext in (".jpg", ".jpeg"):
            with Image.open(src) as im:
                convert = im.getexif().get(0x0112, 1) != 1
        out_ext = ".png" if convert else (".jpg" if ext == ".jpeg" else ext)
        base, n = t.strftime("%Y%m%d-%H%M%S"), 1
        name = base + out_ext
        while name in names:
            n += 1
            name = f"{base}-{n}{out_ext}"
        names.add(name)
        print(f"add:  {name}  <- {src}")
        if a.dry_run:
            continue
        dst = os.path.join(feat.raw, name)
        if ext in (".heic", ".heif"):
            heic_to_png(src, dst)
        elif convert:
            with Image.open(src) as im:
                ImageOps.exif_transpose(im).save(dst, "PNG")
        else:
            shutil.copyfile(src, dst)
        os.chmod(dst, 0o600)
        with Image.open(dst) as im:
            size = im.size
        index.append({"file": name, "source": os.path.abspath(src), "sha256": digest,
                      "taken": t.isoformat(timespec="seconds"), "width": size[0], "height": size[1],
                      "agent": forced})

    if not a.dry_run and picked:
        index.sort(key=lambda e: (e["taken"], e["file"]))
        save_json(feat.index, index)
    print(f"{len(picked)} new image(s), {len(index)} in {feat.raw}" + (" (dry run)" if a.dry_run else ""))


if __name__ == "__main__":
    main()
