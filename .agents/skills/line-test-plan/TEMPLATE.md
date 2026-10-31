# Test plan: <feature> (beeper/line#<issue>)

**Goal:** <the feature in one or two sentences>

## Scope

Copy what the LINE Chrome Extension does, nothing extra. Anything the extension cannot do is out of scope; write down that it cannot.

Known before testing:
- <facts the user already knows, for example which Beeper clients can already do this>

## Platforms

- Chrome extension (LINE Chrome Extension <version>)
- Beeper Desktop
- Beeper mobile
- LINE iOS
- LINE Android

Drop the ones you don't have; list devices and OS versions only as test data allows.

## Test chats

- `<group chat>`: <members, end-to-end encrypted or not>
- `<1:1 chat>`: <...>
- `<chat with a business account or bot>`: <bot type; note it is not a valid media receiver>
- Second account for the receiver's view: <yes, on its own phone / no, see Not testable>

Use test chats. Every change may post a notice that all members see.

**Before A** (when a phone app is involved): put `test/<feature>/samples/` on the phone (AirDrop and save to Files, or Files on Android), so every app attaches the same files.

## Coverage

Who sends (rows) by where you can see it (columns). Cells are step ID ranges; `-` is not applicable; `gap` is listed under Not testable.

| Sender \ Viewer | LINE iOS | LINE Android | Chrome extension | Beeper Desktop |
|---|---|---|---|---|
| Beeper Desktop (before) | A1 | gap | - | A1 |
| LINE iOS (reference) | E1 | gap | C1 | - |
| Beeper Desktop (settings) | E2 | gap | - | - |
| Chrome extension | D1 | gap | B1 | - |
| Beeper Desktop (after) | F1 | gap | - | F1 |

Keep the rows and columns to what the plan has, and make every cell point at a real step.

## Not testable

Known gaps in this run, each with the reason and what would close it:
- <for example: a real second account receiving a bridge-sent copy (no second account); a relay bot is not a valid receiver>

## Test data

Every dummy string used in the tests. `line-evidence` treats these as safe text.

- `<group name>`
- `<message text>`
- `<sample file labels, from samples/MANIFEST.txt>`

## Run order

LINE allows one Chrome Extension session, so the capture logs the bridge out.

1. `before`: Beeper, bridge logged in: <sections>
2. `capture`: log the bridge out, start `line-capture`, then: <sections>. Quit Chrome when done.
3. `after`: deploy, log the bridge in again: <sections>

**Settings record:** (media features) write down the LINE app settings before the first step, and put them back in the last step of the settings section. Bridge log level: <current value>; raise it for the run, restore it after.

**Step IDs are stable:** once a Result is filled, never re-letter or renumber. Add sections and steps with the next free letter or number.

**Pacing:** wait about 10 s between steps (about 15 s after a step on another device). Write what happened in the **Result** column: ✅ worked, ❌ rejected (copy the error text), `n/a` with the reason, plus notes.

## A. Beeper baseline today

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| A1 | Beeper Desktop | before | <action> | <what this shows> | shot: <screen> | |

## B. Sending from Chrome

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| B1 | Chrome extension | capture | <action> | <call, fields, flags> | shot: <screen>; req: <method> | |

## C. Changes made on another device

Keep the extension open on the test chat. Make each change in the LINE app on your phone, then wait about 15 s. This is what the bridge receives.

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| C1 | LINE iOS | capture | <action> | <event type and fields> | shot: Chrome shows it; req: <operation> | |

## D. Edge cases

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| D1 | Chrome extension | capture | <empty input> | blocked in the UI or by the server? | shot: <screen> | |

## E. LINE app settings on the receiving side

Media features only. Settings to consider are listed in `readme/LINE_CLIENT_SETTINGS.md` of the `beeper/line` checkout, when it exists. Change one setting at a time; send one copy from LINE and one from Beeper under each value, and compare. Last step restores the settings.

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| E1 | LINE iOS | before | <setting = value; copy sent from LINE> | <how it arrives> | shot: LINE iOS | |
| E2 | LINE iOS | before | <same setting; copy sent from Beeper> | <same as E1?> | shot: LINE iOS | |

## F. Beeper after deploy

| # | Platform | Phase | Action | Checks | Evidence | Result |
|---|----------|-------|--------|--------|----------|--------|
| F1 | Beeper Desktop | after | <same as A1> | matches Chrome | shot: Beeper; shot: LINE shows it | |

## Findings

[FINDINGS.md](FINDINGS.md) (written by the agent in the analyze phase, after `line-analyze` parses the capture).
