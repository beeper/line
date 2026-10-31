"""Shared helpers for the line-evidence scripts: paths, PLAN.md, allowlist."""

import hashlib
import json
import os
import re
import sys
import unicodedata
from datetime import datetime, timezone

STEP = re.compile(r"^[A-Z][0-9]+$")
PHASES = ("before", "capture", "after")
IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".heic", ".heif", ".webp", ".gif", ".bmp", ".tif", ".tiff"}

# Generic UI text that may stay visible. Phrases are matched word by word
# against OCR lines, so "Group settings" also keeps "Group" and "settings"
# only where they appear together. Keep this list to UI labels and words that
# are never names; feature-specific strings belong in PLAN.md's Test data.
UI_PHRASES = """
Settings|Send|Cancel|Done|OK|Save|Edit|Delete|Remove|Back|Close|Search|Next|Confirm|Change|Apply|Retry
Chats|Chat|Friends|Home|Calls|News|Wallet|Add friends|Groups|Group|Members|Invite|Leave|Leave group
Profile|Profile photo|Group photo|Group name|Group settings|Edit group|Change photo|Remove photo
Choose photo|Select photo|Take photo|Upload|Uploading|Photos|Albums|Notes|Files|Links|Media|Images|Videos
Notifications|Mute|Unmute|Hide|Block|Report|Pin|Unpin|Reply|Forward|Copy|Share|Announcements
Today|Yesterday|Read|Delivered|Sent|Seen|Online|Offline|Typing|New message|Message|Messages
changed the group name to|changed the group profile photo|changed the group photo|removed the group photo
changed the group cover|set the group photo|joined the group|left the group|invited|joined|left
You|you|the|to|a|an|and|or|of|in|on|at|for|from|with|by|is|was|has|have|this|that|new|name|photo|cover
Beeper|LINE|Matrix|Inbox|Archive|Archived|Low priority|Favorites|Unread|All|Spaces|Bridges|Accounts
Letter Sealing|Chat settings|Background|Font size|Language|Help|About|Version|Log out|Logout|Login|Log in
Open|More|Info|Details|Members list|Admin|Owner|Admins|Others|Other|Show more|See all|View all
設定|送信|キャンセル|完了|保存|編集|削除|トーク|友だち|ホーム|グループ|メンバー|招待|退会|写真|アルバム|ノート
"""

# Whole-word patterns that are safe to show: clock times, dates, counts, percent.
SAFE_WORD = re.compile(
    r"^(\d{1,2}:\d{2}(:\d{2})?|[AaPp]\.?[Mm]\.?|\d{1,3}%?|\(\d{1,4}\)|\d{1,2}/\d{1,2}(/\d{2,4})?"
    r"|\d{4}[-/.]\d{1,2}[-/.]\d{1,2}|[^\w]+)$"
)

# PII patterns, applied to the whole OCR line so numbers split by spaces still match.
PII = [
    ("mid", re.compile(r"(?<![0-9A-Za-z])[UCRucr][0-9a-f]{32}(?![0-9A-Za-z])")),
    ("email", re.compile(r"[\w.+-]+@[\w-]+(\.[\w-]+)+")),
    ("phone", re.compile(r"(?<!\w)\+?\d[\d ()./-]{7,}\d(?!\w)")),
]


def norm(s):
    """NFKC, casefold, and strip punctuation at both ends of a word."""
    s = unicodedata.normalize("NFKC", s).casefold().strip()
    return re.sub(r"^[^\w]+|[^\w]+$", "", s) or s


def words(s):
    return [norm(w) for w in s.split() if norm(w)]


def ui_phrases():
    return [p for line in UI_PHRASES.strip().splitlines() for p in line.split("|") if p.strip()]


def die(msg):
    sys.exit(f"error: {msg}")


class Feature:
    """The test/<feature-slug>/ folder and its evidence layout."""

    def __init__(self, path, plan=None):
        self.dir = os.path.abspath(path)
        if not os.path.isdir(self.dir):
            die(f"feature folder not found: {path}")
        self.slug = os.path.basename(self.dir)
        self.evidence = os.path.join(self.dir, "evidence")
        self.raw = os.path.join(self.evidence, "raw")
        self.redacted = os.path.join(self.evidence, "redacted")
        self.boxes = os.path.join(self.evidence, "boxes")
        self.previews = os.path.join(self.raw, "previews")
        self.manifest = os.path.join(self.evidence, "manifest.json")
        self.index = os.path.join(self.raw, "index.json")
        self.plan = plan or os.path.join(self.dir, "PLAN.md")

    def make_raw(self):
        os.makedirs(self.raw, mode=0o700, exist_ok=True)
        os.chmod(self.raw, 0o700)
        # raw/ holds unredacted PII: keep git from ever picking it up.
        ignore = os.path.join(self.raw, ".gitignore")
        if not os.path.exists(ignore):
            with open(ignore, "w") as f:
                f.write("# Unredacted screenshots: never commit or share.\n*\n")

    # Keyed by the full file name, so a.png and a.jpg never share boxes or a preview.
    def box_path(self, image):
        return os.path.join(self.boxes, os.path.basename(image) + ".json")

    def preview_path(self, image):
        return os.path.join(self.previews, os.path.basename(image) + ".png")

    def raw_image(self, name):
        """Resolve an image argument (name, raw/ name or path) to a path inside raw/."""
        for cand in (name, os.path.join(self.raw, name), os.path.join(self.raw, os.path.basename(name))):
            if os.path.isfile(cand) and os.path.dirname(os.path.abspath(cand)) == self.raw:
                return os.path.abspath(cand)
        die(f"not an image in {self.raw}: {name}")

    def rel(self, path):
        return os.path.relpath(path, self.evidence).replace(os.sep, "/")

    def load_index(self):
        return load_json(self.index, [])


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def numbered(data):
    """Every box with its state, sorted top to bottom and numbered from 1."""
    out = []
    for b in data["auto"]:
        out.append(dict(b, state="keep" if b["id"] in data.get("unhide", []) else "cover"))
    for b in data["kept"]:
        out.append(dict(b, state="cover" if b["id"] in data.get("hide", []) else "keep"))
    for i, b in enumerate(data.get("add", [])):
        out.append(dict(b, id=f"add{i}", kind="added", state="add"))
    out.sort(key=lambda b: (b["y"], b["x"]))
    for n, b in enumerate(out, 1):
        b["n"] = n
    return out


def covers(data):
    return [b for b in numbered(data) if b["state"] in ("cover", "add")]


def rect(b, size):
    """Padded rectangle, clamped to the image."""
    pad = 2 if b.get("state") == "add" else max(2, round(min(b["w"], b["h"]) * 0.15))
    W, H = size
    return (max(0, b["x"] - pad), max(0, b["y"] - pad), min(W, b["x"] + b["w"] + pad), min(H, b["y"] + b["h"] + pad))


def approval_hash(data, image):
    """Hash of the raw image's bytes and the covered rectangles.

    An approval is only valid while this matches: replacing the screenshot or
    changing any box voids it.
    """
    rects = sorted(rect(b, (data["width"], data["height"])) for b in covers(data))
    return hashlib.sha256(json.dumps([sha256_file(image), rects]).encode()).hexdigest()[:16]


def is_approved(data, image):
    return bool(data) and (data.get("approved") or {}).get("hash") == approval_hash(data, image)


def load_json(path, default):
    if not os.path.exists(path):
        return default
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_json(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False, sort_keys=False)
        f.write("\n")
    os.replace(tmp, path)


def cells(line):
    return [c.strip() for c in line.strip().strip("|").split("|")]


def parse_plan(path):
    """Return (test_data, steps, date) from a PLAN.md.

    test_data: strings from `## Test data` bullets (backticked parts if any,
    else the whole bullet). steps: {id: {"platform", "phase"}} from tables with
    #, Platform and Phase columns. date: first YYYY-MM-DD near the top, or None.
    A missing plan returns empty values, so older plans still work with --allow.
    """
    if not path or not os.path.exists(path):
        return [], {}, None
    lines = open(path, encoding="utf-8").read().splitlines()
    test_data, steps, header, section = [], {}, None, ""
    date = None
    for l in lines[:30]:
        m = re.search(r"\b(20\d\d-\d\d-\d\d)\b", l)
        if m:
            date = m.group(1)
            break
    for l in lines:
        if l.startswith("## "):
            section, header = l.strip(), None
            continue
        if section == "## Test data" and l.lstrip().startswith(("- ", "* ")):
            body = l.lstrip()[2:].strip()
            ticks = re.findall(r"`([^`]+)`", body)
            for t in ticks or [body]:
                if t.strip() and t.strip().lower() != "none":
                    test_data.append(t.strip())
        if not l.lstrip().startswith("|"):
            header = None if not l.strip() else header
            continue
        c = cells(l)
        if c and c[0] == "#":
            header = c
            continue
        if not header or set("".join(c)) <= set("-: ") or len(c) != len(header):
            continue
        row = dict(zip(header, c))
        if STEP.match(row.get("#", "")) and "Platform" in row and "Phase" in row:
            steps[row["#"]] = {"platform": row["Platform"], "phase": row["Phase"]}
    return test_data, steps, date


def slug(s):
    s = unicodedata.normalize("NFKD", s).encode("ascii", "ignore").decode().lower()
    return re.sub(r"[^a-z0-9]+", "-", s).strip("-") or "unknown"


def now_iso():
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")
