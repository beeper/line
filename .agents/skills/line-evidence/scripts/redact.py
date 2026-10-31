#!/usr/bin/env python3
"""Preview, approve and write redacted screenshots from evidence/boxes/<image>.json.

Usage:
  redact.py --feature test/<slug> preview IMAGE...   # raw/previews/<image>.png + raw/review.html
  redact.py --feature test/<slug> approve IMAGE...   # record the user's OK for the current boxes
  redact.py --feature test/<slug> final IMAGE...     # redacted/<step>-<platform>-<phase>.png

Boxes that get covered: every "auto" box whose id is not in "unhide", every
"kept" box whose id is in "hide", and every "add" rectangle
({"x", "y", "w", "h", "label"} in pixels). Covers are solid filled boxes,
never blur, padded a little so letter edges do not show.

The preview puts the original with numbered outlines (red: covered, green:
left visible, blue: added by hand) next to the redacted result, and prints
the number -> id table. Previews contain raw pixels and stay in raw/.

`approve` stores a hash of the raw image's bytes and the covered
rectangles; replacing the screenshot or editing any box voids it, and every
script then deletes the redacted output made under the old approval.
`final` refuses images without a matching approval or without a manifest
entry (manifest.py pick), re-encodes to PNG
with every piece of metadata dropped (EXIF, GPS, text chunks, ICC after
converting to sRGB), OCRs the result again as a check, and records it in
manifest.json.
"""

import argparse
import html
import io
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from evidence_lib import (Feature, approval_hash, covers, die, is_approved, load_json, now_iso,  # noqa: E402
                          numbered, parse_plan, rect, save_json, sha256_file)

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    die("Pillow is missing; run the preflight")

FILL = (24, 24, 24)
COLORS = {"cover": (230, 40, 40), "keep": (30, 170, 60), "add": (40, 110, 240)}


def load_boxes(feat, image):
    path = feat.box_path(image)
    data = load_json(path, None)
    if data is None:
        die(f"no boxes for {os.path.basename(image)}; run detect.py first")
    return path, data


def render(im, data):
    out = im.convert("RGB")
    draw = ImageDraw.Draw(out)
    for b in covers(data):
        draw.rectangle(rect(b, out.size), fill=FILL)
    return out


def font(size):
    for name in ("Arial Bold.ttf", "Arial.ttf", "DejaVuSans-Bold.ttf", "arialbd.ttf", "arial.ttf"):
        try:
            return ImageFont.truetype(name, size)
        except OSError:
            pass
    return ImageFont.load_default()


def preview(feat, image):
    _, data = load_boxes(feat, image)
    with Image.open(image) as im:
        im.load()
    marked = im.convert("RGB")
    draw = ImageDraw.Draw(marked)
    f = font(max(12, im.size[0] // 70))
    boxes = numbered(data)
    for b in boxes:
        r = rect(b, im.size)
        draw.rectangle(r, outline=COLORS[b["state"]], width=max(2, im.size[0] // 500))
        draw.text((r[0], max(0, r[1] - f.size - 2)), str(b["n"]), fill=COLORS[b["state"]], font=f)
    after = render(im, data)
    gap = 24
    sheet = Image.new("RGB", (im.size[0] * 2 + gap, im.size[1]), (255, 255, 255))
    sheet.paste(marked, (0, 0))
    sheet.paste(after, (im.size[0] + gap, 0))
    os.makedirs(feat.previews, mode=0o700, exist_ok=True)
    out = feat.preview_path(image)
    sheet.save(out)
    print(f"{os.path.basename(image)} -> {out}")
    for b in boxes:
        what = b.get("text") or b.get("label") or (f"{b.get('chars')} chars" if b.get("chars") else "")
        print(f"  {b['n']:3} {b['state']:5} {b['kind']:9} {b['id']:9} {b['x']},{b['y']} {b['w']}x{b['h']}  {what}")
    if not data.get("faces_checked"):
        print("  faces NOT checked by a detector: look for faces and avatars yourself")


def write_review(feat, items):
    rows = []
    for image, out, boxes, data in items:
        approved = is_approved(data, image)
        table = "".join(
            f"<tr class={b['state']}><td>{b['n']}</td><td>{b['state']}</td><td>{html.escape(b['kind'])}</td>"
            f"<td><code>{b['id']}</code></td><td>{html.escape(b.get('text') or b.get('label') or '')}</td></tr>"
            for b in boxes)
        rows.append(
            f"<section><h2>{html.escape(os.path.basename(image))} "
            f"<small>{'approved' if approved else 'not approved'}</small></h2>"
            f"<img src='previews/{html.escape(os.path.basename(out))}'>"
            f"<details><summary>{len(boxes)} boxes</summary><table>{table}</table></details></section>")
    page = f"""<!doctype html><meta charset=utf-8><title>Redaction review</title>
<style>body{{font:14px system-ui;margin:16px;background:#fafafa;color:#222}} img{{max-width:100%;border:1px solid #ccc}}
section{{margin:0 0 40px}} small{{color:#888;font-weight:normal}} td{{padding:2px 8px}}
tr.cover td:nth-child(2){{color:#c22}} tr.keep td:nth-child(2){{color:#1a2}} tr.add td:nth-child(2){{color:#26e}}
.warn{{background:#fee;padding:8px;border:1px solid #e99}}</style>
<p class=warn>Local file with unredacted screenshots. Do not upload, publish or share it.
Left: original with numbered boxes (red covered, green visible, blue added). Right: what will be published.</p>
{''.join(rows)}"""
    path = os.path.join(feat.raw, "review.html")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(page)
    print(f"review page: {path}")


def to_srgb(im):
    icc = im.info.get("icc_profile")
    if not icc:
        return im
    try:
        from PIL import ImageCms

        src = ImageCms.ImageCmsProfile(io.BytesIO(icc))
        mode = "RGBA" if im.mode in ("RGBA", "LA", "P") else "RGB"
        return ImageCms.profileToProfile(im.convert(mode), src, ImageCms.createProfile("sRGB"), outputMode=mode)
    except Exception as e:
        print(f"  note: could not convert ICC profile to sRGB ({e}); dropping it", file=sys.stderr)
        return im


def final(feat, image, manifest_mod):
    path, data = load_boxes(feat, image)
    size = (data["width"], data["height"])
    # Drop outputs whose approval no longer holds before anything else.
    m = manifest_mod.sweep(feat)
    if not is_approved(data, image):
        die(f"{os.path.basename(image)} is not approved for its current image and boxes; "
            "preview it and get the user's OK")
    step, entry = manifest_mod.find(m, feat.rel(image))
    if not entry:
        die(f"{os.path.basename(image)} is not in the manifest; run manifest.py pick first")
    with Image.open(image) as im:
        im.load()
        if im.size != size:
            die(f"{os.path.basename(image)} is {im.size}, boxes were made for {size}; run detect.py again")
        im = to_srgb(im)
        alpha = "A" in im.getbands() or (im.mode == "P" and "transparency" in im.info)
        mode = "RGBA" if alpha else "RGB"
        base = im.convert(mode)
    drawn = base.copy()
    draw = ImageDraw.Draw(drawn)
    for b in covers(data):
        draw.rectangle(rect(b, size), fill=FILL + ((255,) if mode == "RGBA" else ()))
    # Rebuild from raw pixels so no info, EXIF, ICC or text chunk can survive.
    clean = Image.frombytes(mode, size, drawn.tobytes())
    if entry.get("redacted"):
        manifest_mod.drop_output(feat, entry)
    rel = manifest_mod.output_name(m, step, entry)
    out = os.path.join(feat.evidence, rel)
    os.makedirs(feat.redacted, exist_ok=True)
    clean.save(out, "PNG", optimize=True)
    with Image.open(out) as check:
        leftover = set(check.info) - {"dpi", "gamma", "transparency"}
        if leftover or check.getexif():
            os.remove(out)
            die(f"metadata survived in {out}: {sorted(leftover)}")
    verify(feat, out, data)
    entry.update(redacted=rel, approved=True, approval=data["approved"]["hash"], redacted_sha256=sha256_file(out))
    manifest_mod.save(feat, m)
    print(f"{os.path.basename(image)} -> {out}")


def verify(feat, out, data):
    """OCR the redacted file and warn about text that would have been hidden."""
    import detect

    test_data, _, _ = parse_plan(feat.plan)
    try:
        _, result = detect.analyze(out, "auto")
    except SystemExit:
        print("  check skipped: no OCR engine", file=sys.stderr)
        return
    auto, _ = detect.classify(result, test_data, data.get("allow", []))
    unhidden = set(data.get("unhide", []))
    stray = [b for b in auto if b["id"] not in unhidden and b["kind"] != "face"]
    stray = [b for b in stray if not any(_inside(b, u) for u in data["auto"] if u["id"] in unhidden)]
    faces = [b for b in auto if b["kind"] == "face"]
    if stray or faces:
        print(f"  CHECK: OCR still finds {len(stray)} hidden-class text box(es) and {len(faces)} face(s) in the output:")
        for b in stray + faces:
            print(f"    {b['kind']} at {b['x']},{b['y']} {b['w']}x{b['h']}")
        print("  Look at the output before using it; add boxes and redo if anything is readable.")


def _inside(a, b):
    return a["x"] >= b["x"] - 4 and a["y"] >= b["y"] - 4 and a["x"] + a["w"] <= b["x"] + b["w"] + 4 \
        and a["y"] + a["h"] <= b["y"] + b["h"] + 4


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--feature", required=True)
    ap.add_argument("--plan")
    ap.add_argument("cmd", choices=["preview", "approve", "final"])
    ap.add_argument("images", nargs="+")
    a = ap.parse_args()
    feat = Feature(a.feature, a.plan)
    images = [feat.raw_image(i) for i in a.images]
    if a.cmd == "preview":
        for image in images:
            preview(feat, image)
        # The review page lists every image that has a preview, not just this run's.
        items = []
        for e in feat.load_index() or [{"file": f} for f in sorted(os.listdir(feat.raw))]:
            image = os.path.join(feat.raw, e["file"])
            out = feat.preview_path(image)
            if os.path.exists(out) and os.path.exists(feat.box_path(image)):
                _, data = load_boxes(feat, image)
                items.append((image, out, numbered(data), data))
        write_review(feat, items)
    elif a.cmd == "approve":
        for image in images:
            path, data = load_boxes(feat, image)
            data["approved"] = {"hash": approval_hash(data, image), "at": now_iso()}
            save_json(path, data)
            print(f"approved {os.path.basename(image)} ({len(covers(data))} covers)")
        # A new approval makes any output written under the old one stale.
        import manifest

        manifest.sweep(feat)
    else:
        import manifest

        for image in images:
            final(feat, image, manifest)


if __name__ == "__main__":
    main()
