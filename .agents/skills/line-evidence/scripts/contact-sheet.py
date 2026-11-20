#!/usr/bin/env python3
"""Build raw/contact-sheet.html: numbered thumbnails of the collected screenshots.

Usage:
  contact-sheet.py --feature test/<slug> [--guesses guesses.json]

Numbers follow raw/index.json (oldest first), so "3, 7, 12" from the user maps
back to files. Each card shows a guessed platform from the image shape and
file name. --guesses is a JSON object the agent writes after looking at the
images, {"3": {"platform": "LINE iOS", "step": "B5", "phase": "after"}, ...};
it replaces the shape guess. The page uses relative links to the raw files
and is meant to be opened from disk only: it shows unredacted PII.
"""

import argparse
import html
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from evidence_lib import Feature, die, load_json  # noqa: E402


def shape_guess(e):
    w, h = e.get("width") or 0, e.get("height") or 0
    src = os.path.basename(e.get("source", ""))
    if h and h / max(w, 1) > 1.9:
        return "phone (LINE iOS or Beeper mobile)"
    if src.upper().startswith("IMG_"):
        return "phone"
    if w and h and w <= 900 and h <= 1100:
        return "desktop window (Chrome extension?)"
    return "desktop (Beeper Desktop or Chrome)"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--feature", required=True)
    ap.add_argument("--guesses")
    a = ap.parse_args()
    feat = Feature(a.feature)
    index = feat.load_index()
    if not index:
        die(f"nothing collected in {feat.raw}; run collect.py first")
    guesses = load_json(a.guesses, {}) if a.guesses else {}

    cards = []
    for n, e in enumerate(index, 1):
        g = guesses.get(str(n), {})
        platform = g.get("platform") or shape_guess(e)
        step = " ".join(x for x in (g.get("step"), g.get("phase")) if x) or "?"
        tag = " · agent" if e.get("agent") else ""
        src = html.escape(e["file"])
        cards.append(
            f"<figure><a href='{src}' target=_blank><img loading=lazy src='{src}'></a>"
            f"<figcaption><b>{n}</b> <span class=step>{html.escape(step)}</span><br>"
            f"{html.escape(platform)}<br><small>{html.escape(e['taken'].replace('T', ' '))} · "
            f"{e.get('width')}×{e.get('height')}{tag}</small></figcaption></figure>")

    page = f"""<!doctype html><meta charset=utf-8><title>Contact sheet: {html.escape(feat.slug)}</title>
<style>body{{font:14px system-ui;margin:16px;background:#fafafa;color:#222}}
main{{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:16px}}
figure{{margin:0;background:#fff;border:1px solid #ddd;border-radius:6px;padding:8px}}
img{{width:100%;height:260px;object-fit:contain;background:#eee}} b{{font-size:20px}}
.step{{background:#eef;padding:1px 6px;border-radius:4px}} small{{color:#777}}
.warn{{background:#fee;padding:8px;border:1px solid #e99}}</style>
<p class=warn>Local file with unredacted screenshots. Do not upload, publish or share it.</p>
<h1>{html.escape(feat.slug)}: {len(index)} screenshots</h1>
<p>Reply with the numbers to use and what they show, for example: <code>3, 7, 12; 7 is B5 after</code>.</p>
<main>{''.join(cards)}</main>"""
    out = os.path.join(feat.raw, "contact-sheet.html")
    with open(out, "w", encoding="utf-8") as f:
        f.write(page)
    os.chmod(out, 0o600)
    for n, e in enumerate(index, 1):
        print(f"{n:3}  {e['file']}  {guesses.get(str(n), {}).get('platform') or shape_guess(e)}")
    print(f"contact sheet: {out}")


if __name__ == "__main__":
    main()
