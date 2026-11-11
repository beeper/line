---
name: line-evidence
description: Turns the screenshots from a beeper/line feature test into PR-ready evidence with personal information covered. Collects screenshots from local folders, builds a local contact sheet to pick from, finds text, phone numbers, emails, LINE MIDs and faces with on-device OCR, covers everything not on the test-data allowlist with solid boxes, previews before/after for approval, and writes metadata-free PNGs plus a manifest. Use after testing a feature on the Chrome extension, Beeper Desktop, Beeper mobile or LINE iOS, or when asked to redact, collect or prepare screenshots or test evidence for a PR.
---

# LINE Evidence

Turns a feature's test screenshots into evidence for a PR, with every name,
avatar, face, phone number, email and MID covered. Run the commands below
from the repository root. The scripts are Python 3.8+ with Pillow and run the
same on macOS, Linux and Windows (Git Bash).

## Hard rule: raw images stay on this machine

Raw screenshots, the contact sheet, previews and any image not yet approved
show real contacts and chats. They never leave the machine: no artifacts, no
PR or issue uploads, no chat attachments to other services, no pastebins or
image hosts, no commits. Open them only from disk. Looking at them yourself
to find PII is part of this skill; sending them anywhere else is not. Only
approved files in `evidence/redacted/` may be uploaded later, by the user,
taking the list from `manifest.py eligible`.

## Layout

Shared with `line-test-plan`: the feature folder is `test/<feature-slug>/`,
and its `PLAN.md` has step IDs like `B5` (`^[A-Z][0-9]+$`), a `Platform` and a
`Phase` (`before` | `capture` | `after`) column, and a `## Test data` section
of dummy strings.

```
test/<feature-slug>/
  PLAN.md
  evidence/
    raw/                 never shared (git-ignored by its own .gitignore)
      index.json         collected files, their source and time
      contact-sheet.html
      previews/, review.html
    boxes/<image file>.json  boxes per raw image, e.g. 20261025-044601.png.json; no hidden text
    redacted/<step>-<platform>-<phase>.png
    manifest.json        step -> files, platform, phase, approved, in_pr
```

Below, `F=test/<feature-slug>` and `S=.agents/skills/line-evidence/scripts`.
Use `python3`, or the Python the preflight reported.

## Workflow

1. **Check dependencies.** Run `bash $S/line-evidence-preflight.sh`. It prints
   `OK`, `MISSING` (required) and `OPTIONAL` (works, with a gap) lines and no
   install commands. Pick commands that fit the OS and its package managers
   (for example `<python> -m pip install --user Pillow`, Homebrew, apt,
   winget), show them, and install only after the user confirms. On macOS,
   Apple Vision does OCR and faces. Elsewhere tesseract does OCR, and faces need
   `opencv-python-headless`; without it, tell the user faces are found only by
   looking, and look extra carefully.

2. **Collect.** Ask where the screenshots are, suggesting `~/Downloads` and
   `~/Desktop` (AirDrop, downloads and shares land there); accept several
   paths. Ask for the time window of the test session, or let the script take
   it from the date in `PLAN.md` (that day 00:00 until now) and tell the user
   which window it used.
   ```bash
   python3 $S/collect.py --feature $F --since "2026-10-25 03:00" ~/Downloads ~/Desktop
   python3 $S/collect.py --feature $F --add /path/to/agent-screenshot.png   # screenshots you took yourself
   ```
   It keeps only images in the window, skips duplicates by SHA-256, turns
   HEIC and rotated JPEGs into upright PNGs, and copies them to
   `evidence/raw/`. Use `--dry-run` first when the folders are large.

3. **Pick.** Look at the collected images, write your guesses as
   `{"<number>": {"platform", "step", "phase"}}` to a scratch file, and build
   the contact sheet:
   ```bash
   python3 $S/contact-sheet.py --feature $F --guesses /tmp/guesses.json
   ```
   Give the user the path to `evidence/raw/contact-sheet.html` to open from
   disk. **Never publish it**: no artifact, no upload, no file card to another
   service. The user answers like `3, 7, 12; 7 is B5 after`. Record each pick
   (platform and phase default to the step's row in `PLAN.md`):
   ```bash
   python3 $S/manifest.py --feature $F pick --step B5 --phase after 20261025-044601.png
   ```

4. **Find PII** in each picked image:
   ```bash
   python3 $S/detect.py --feature $F 20261025-044601.png [--allow "LSON Group" ...]
   ```
   This runs on-device OCR (Vision on macOS, else tesseract) and face
   detection, and checks phone numbers, emails and LINE MIDs
   (`[UCR][0-9a-f]{32}`). Every word is covered unless it is in `PLAN.md`'s
   `## Test data`, an `--allow` string (kept in the boxes file for re-runs), or
   the built-in generic UI list in `evidence_lib.py`. Then **look at each image
   yourself**: OCR misses avatars, photos of people, stylized names, QR codes,
   phone status bars and text inside images. Add a box for each of those (step 5).

5. **Preview and adjust.**
   ```bash
   python3 $S/redact.py --feature $F preview 20261025-044601.png
   ```
   This writes `evidence/raw/previews/<image file>.png` (left: numbered boxes, red
   covered, green visible, blue added; right: the result) and
   `evidence/raw/review.html`, and prints the numbered box list. Look at the
   preview yourself, then give the user the `review.html` path (local only).
   Apply what they say by editing `evidence/boxes/<image file>.json`:
   - "unhide 4": add box 4's `id` to `unhide` (for a red box).
   - "hide 9": add box 9's `id` to `hide` (for a green box).
   - "also hide the top bar": append `{"x", "y", "w", "h", "label"}` in pixels
     to `add`, measured from the image.
   Re-run `preview` after each change. Boxes are solid fills, never blur.
   Re-running `detect.py` keeps `add`, `hide`, `unhide` and `allow`.

6. **Approve and write.** Only after the user says the preview is fine:
   ```bash
   python3 $S/redact.py --feature $F approve 20261025-044601.png
   python3 $S/redact.py --feature $F final 20261025-044601.png
   ```
   `approve` stores a hash of the raw image's bytes and the covered boxes.
   Replacing the screenshot or changing any box voids it, and the scripts then
   delete the redacted file made under the old approval, so `redacted/` only
   ever holds currently approved output.
   `final` needs the approval and a manifest entry, re-encodes to PNG from bare
   pixels (no EXIF, GPS, text chunks or ICC profile), writes
   `evidence/redacted/<step>-<platform>-<phase>.png`, OCRs the result again and
   warns about readable text, and marks the entry approved in
   `manifest.json`. Open each output yourself before calling it done.

7. **Report.** Run `python3 $S/manifest.py --feature $F show` and list the
   redacted files per step. `manifest.py eligible` prints only files that are
   approved, unchanged on disk and marked for the PR: that is the upload list. `manifest.py set --in-pr no <step|file>` leaves a
   file out of the PR. Tell the user that `evidence/raw/` still holds the
   unredacted originals and ask whether to delete it once the PR is merged.

## Notes

- The generic UI allowlist is deliberately short. Feature strings belong in
  `PLAN.md`'s `## Test data`, not in the script. Plans older than
  `line-test-plan` have no such section: pass their dummy strings with
  `--allow`.
- Hidden text is never written to `boxes/*.json`; only its kind and length.
  Visible (allowlisted) text is.
- A screenshot with nothing to show after redaction is not evidence. Say so,
  and suggest retaking it with test data instead of real chats.
