---
name: line-test-plan
description: Writes the numbered test plan (PLAN.md) for a beeper/line bridge feature before any capture or code, with lettered sections, step IDs, phases (before, capture, after) and Result columns, plus labeled sample media when the feature needs files. Use when starting a new LINE bridge feature, when asked for a test plan or capture plan, or before running line-capture.
---

# LINE Test Plan

Writes `test/<feature-slug>/PLAN.md` inside a `beeper/line` checkout: the list of
things to do in LINE, Beeper and the Chrome Extension, numbered so every
screenshot, request and result can point at a step. It comes before capture and
code. Run the commands below from the repository root.

## Contract

Other skills read this layout (`line-evidence` relies on it), so keep it exact.

- Feature folder: `test/<feature-slug>/` in the checkout. `/test` is gitignored there; nothing in it is committed.
- `PLAN.md` holds, in this order:
  1. title, goal, and the issue/PR link
  2. **Scope**: copy the LINE Chrome Extension, nothing extra
  3. **Platforms** involved (Chrome extension, Beeper Desktop, Beeper mobile, LINE iOS, LINE Android) and **Test chats** (which chats and accounts exist)
  4. **Coverage** and **Not testable** (known gaps): see "Coverage and gaps" below
  5. `## Test data`: a bullet per dummy string used in tests (group names, message text, sample file labels). `line-evidence` treats this as the allowlist of text that is not PII
  6. `## Run order`: which sections run in which phase
  7. lettered sections `## A. Title`, each with one table
  8. `## Findings`: a link to `FINDINGS.md` (the agent writes it in the analyze phase, after `line-analyze` has parsed the capture; the plan only links to it)
- Step IDs match `^[A-Z][0-9]+$` (`A1`, `B12`) and are unique across the plan.
- Every table has exactly these columns: `#`, `Platform`, `Phase`, `Action`, `Checks`, `Evidence`, `Result`.
  - `Phase` is `before`, `capture` or `after`.
  - `Evidence` names the screenshots and requests the step should produce, for example `shot: Beeper shows the GIF; req: sendMessage`.
  - `Result` starts empty.
- Step IDs are stable. Once any `Result` cell is filled, never re-letter or renumber sections or steps: results, screenshots, `FINDINGS.md` and `line-evidence` all point at the IDs. Add new sections with the next free letter, and new steps with the next free number in their section (`A3b` is not valid: use `A9`). Mark a dropped step `n/a` with the reason instead of deleting it. Re-lettering is fine only while no results exist.
- Sample media lives in `test/<feature-slug>/samples/`.

[TEMPLATE.md](TEMPLATE.md) is a skeleton with every part.

## What to do

1. **Ask for the goal and the issue or PR.** One question: the feature in a
   sentence, and the `beeper/line` issue or PR number (read it with `gh issue
   view` / `gh pr view`). Pick a short `<feature-slug>` (`group-avatar`,
   `send-gifs`) and confirm it.

2. **Ask about the test setup.** The shape of the plan depends on this, and
   guessing it cost the first real run (GIF sending) three rewrites. Ask in one
   message, and write the answers into `## Platforms` and `## Test chats`:
   - **Chats and accounts:** which test chats exist: a group, a 1:1 chat, a chat with a business account or bot. Whether the chats are end-to-end encrypted (Letter Sealing) decides the upload path, so say which is which.
   - **Devices:** which are available: Chrome extension, Beeper Desktop, Beeper iOS/Android, LINE iOS, LINE Android.
   - **Second account:** is there a second real LINE account, ideally on its own phone, for the receiver's view? If not, that is a known gap (see "Sender vs. receiver").
   - **What the user already knows:** for example which Beeper clients can already do the thing, known limits, earlier findings. Write these into `## Scope` as "Known before testing" so no step re-tests them.

3. **Read what exists, then propose.**
   - `AGENTS.md`/`CLAUDE.md`, `docs/protocol/`, and the code the feature touches in `pkg/line/` and `pkg/connector/`.
   - Every `test/*/FINDINGS.md` of related features: what the extension already
     enforces (limits, formats, rules) tells you which cases are known and which
     still need a capture.
   - Then show the user the proposed sections as a short list (letter, title,
     phase, platform, number of steps) and let them add, drop or reorder before
     you write steps. Do not write the full plan first.

4. **Sections to propose.** Always include:
   - **Beeper baseline** (`before`): how the feature behaves today through the bridge, so "after" has something to compare with.
   - **LINE's own clients as the reference** (`before`): do the same thing from LINE iOS or Android (the same sample file, the same chat) and look at the result in every client. This needs no capture and is often the best reference for what "correct" looks like. Not everything on the LINE side needs the Chrome capture; use the capture only for what the Chrome extension sends or receives on the wire.
   - **Sending from Chrome** (`capture`): one step per variant the extension offers.
   - **Changes made on another device** (`capture`): the user does it in LINE on their phone while the extension stays open; this is what the bridge receives. Include LINE iOS and Android if both are available. Add a step for "undo it" so the test chat is tidy.
   - **Edge cases** (`capture`): limits (length, size, count), empty or unchanged input, cancel before sending, unsupported formats (greyed out in a picker is a result too), unicode and emoji, group vs. 1:1 chat, Letter Sealing on vs. off.
   - **Beeper after deploy** (`after`): the same actions as the baseline, plus the edge cases that matter, plus changes made on LINE iOS/Android arriving in Beeper (Desktop and mobile).
   - **LINE app settings on the receiving side** (for media features: images, GIFs, video, files). See "LINE app settings" below.
   - Add sections the feature needs (media formats, reactions, permissions). Cover only what the extension does.
   - Prefer one phase per section. If a topic spans phases, split it into two sections and cross-reference them.

5. **Order the phases around the single Chrome Extension session.** LINE allows
   one extension session at a time, so a capture logs the bridge out (and
   running the bridge logs the capture out).
   1. `before`: Beeper steps, bridge logged in.
   2. `capture`: the user logs the bridge out, starts `line-capture`, and does every Chrome step and every other-device step without breaking the session; then quits Chrome.
   3. `after`: the feature is deployed, the bridge is logged in again, and the Beeper steps run on the new build.
   Write this into `## Run order` (phase → sections). Within a table, rows follow the order the user performs them.

6. **Write the steps.** Each one is a single action a person can do in about 10
   seconds, with a `Checks` cell that says what the step is for ("which
   attribute flag", "is the 60-character name cut?") and an `Evidence` cell with
   the expected screenshot or request. Use only dummy strings, and list every
   one in `## Test data`. Never put real names, MIDs, phone numbers or tokens
   in the plan.

7. **Sample media, when the feature needs files.**
   - Run `bash .agents/skills/line-test-plan/scripts/line-test-plan-preflight.sh` (on Windows, from Git Bash). It prints `OK`, `MISSING` (required) or `OPTIONAL` per dependency and does not say how to install anything: choose the install method that fits the detected OS and package managers (for example `<python> -m pip install --user Pillow`), show the commands, ask the user to confirm, install, and re-run until it exits 0.
   - Use the Python the preflight found (`py -3` on Windows only; a `PYTHON` override is respected): `eval "$(bash .agents/skills/line-test-plan/scripts/line-test-plan-preflight.sh --print-env)"` sets the `PYTHON` array, and the commands below run it as `"${PYTHON[@]}"`.
   - Generate labeled samples:
     ```bash
     "${PYTHON[@]}" .agents/skills/line-test-plan/scripts/make-samples.py test/<feature-slug>/samples --kinds image,animated
     ```
     `--kinds` takes `image`, `animated`, `video`, `shape`, `ladder`; `--only 10,21` picks files by number. The animated set includes a transparent GIF, a tiny 32×32 one, and wide, tall and square ones. `ladder` is the size ladder (about 5, 10, 20 and 27 MB of animated GIF, about 60 MB in all, so it is not built by default): use `--kinds ladder` or `--only 28,29,30,24`, and send the ladder smallest first. Every file shows its own name and corner markers, so a screenshot proves which file was sent and how it was cropped. (Samples under 64 px show only their number, such as `23`, because the name does not fit; `MANIFEST.txt` maps it to the file.)
   - It runs on macOS, Linux and Windows with Python 3.8+ and Pillow. Video (MP4, HEVC, MOV, WebM) needs `ffmpeg`; HEIC and AVIF need `pillow-heif` or an `ffmpeg` with the encoder. Whatever it cannot encode is listed as skipped: tell the user, and do not put those files in the plan.
   - Put each file name in the plan's steps and its label (from `samples/MANIFEST.txt`) in `## Test data`.

8. **Validate and hand over.** Run `"${PYTHON[@]}" .agents/skills/line-test-plan/scripts/check-plan.py test/<feature-slug>/PLAN.md` (set `PYTHON` as in step 7; do it here too if the feature has no media) and fix what it reports. Then tell the user how to run it:
   - **Pacing:** do one step, wait about 10 seconds, then the next. The gaps let the analysis match each step to its network calls. Other-device steps need about 15 seconds before the next one.
   - **Result column:** say it as you go ("A3 saved", "B4 greyed out", "C2 error: ..."). Mark `✅` accepted or working, `❌` rejected or broken (copy the error text), `n/a` with the reason when the step cannot be done, and add notes on cropping, animation or timing.
   - **Order:** all `before` steps, then log the bridge out and start the capture for the `capture` steps, then deploy and run the `after` steps.

9. **Fill results as the user reports them.** When the user says what happened,
   edit the matching `Result` cell right away, with the step ID. If a report
   does not match any step, ask. Keep errors verbatim, and leave a step empty
   rather than guessing. When the plan is finished, point to `line-capture` for
   the capture phase and `line-analyze` to parse it; the agent then writes `FINDINGS.md` from that analysis.

## Coverage and gaps

Put both in the plan, after `## Test chats` and before `## Test data`.

- **Coverage table:** who sends (rows) by where you can see it (columns). For example senders Beeper Desktop, Beeper mobile, LINE iOS, Chrome extension, bot relay; viewers LINE iOS, LINE Android, Chrome extension, Beeper Desktop, Beeper mobile. Each cell is a step ID range, `-` for not applicable, or `gap` for not testable. A reader sees at a glance what is covered.
- **Known gaps / not testable:** a bullet per thing this run cannot check, with the reason (no second account, no Android device, no way to send it) and what would close it. Gaps are findings too; carry them into `FINDINGS.md`.

## Sender vs. receiver

LINE can show your own sends differently from what others get. For example LINE iOS only autoplayed GIFs sent from that phone, while a GIF sent from another device of the same account (the Chrome extension or the bridge) needed a tap, so the sender's own view proves little.

- A real second LINE account on its own phone is the true receiver test. Plan the receiver steps for it, and list them under gaps when there is none.
- A Messaging API relay bot is **not** a valid receiver for media. It re-sends the file with its own metadata and can flatten things (GIFs lose animation attributes), so what you see is the bot's message, not the bridge's. Use it only to get a received-message view, and say so in the plan.
- The Chrome extension opened on the chat is a received-message view with no local file cache; it is a useful second opinion, not a replacement for a second account.

## LINE app settings

For media features add a section of the receiving side's LINE app settings (autoplay, auto-download, photo quality, and so on). If `readme/LINE_CLIENT_SETTINGS.md` exists in the checkout, read it and use it as the list of settings to consider; if not, take the list from the app's Settings screens. Do not invent settings.

- Pick only those that can change what the feature shows.
- Before the run, write down each setting's current value (a `Settings record` line under `## Run order`), and end the section with a step that puts them back.
- For each setting value, send one copy from LINE and one from Beeper (or the bridge) and compare how each arrives. Change one setting at a time.

## Sample files on the phone

When a mobile app is involved (Beeper or LINE on iOS or Android), add a step at the start of the first section that uses it: put `test/<feature-slug>/samples/` on the phone (AirDrop to the iPhone and save to Files; on Android, a cable, Files, or a cloud folder), so both apps attach the same files. Without it the phone sends other files than the ones named in the plan.

## Recording bridge logs

Raise the bridge's log level (to debug) for the run, and note the current value in the plan so it can be restored. Start the bridge's log output into a file under `test/<feature-slug>/` before the `before` and `after` phases, and stop and restore the level when the phase ends. Say in the plan how the logs are read (where they go, which time window per step). Never paste logs into the plan or a PR without redaction.

## Visual overview (optional)

Offer a one-page visual overview of the plan: the phases, sections, coverage table and gaps, built from `PLAN.md` as a single self-contained HTML file. Ask first; many people don't need one. Use only the plan's dummy test data, no real names, MIDs, numbers or screenshots. Keep it local in the feature folder, and publish it (for example as an Artifact) only when the user says so.

Never commit the feature folder, and keep captures, tokens, MIDs and real
account data out of the plan.
