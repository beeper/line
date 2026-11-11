---
name: line-beeper-api
description: Sends test messages and media into allowlisted LINE chats through Beeper Desktop's local API, and reads back exactly what Beeper received (type, mimetype, size, width/height, GIF flag, file hash, frame count), with before/after snapshots and diffs. Use during the before and after phases of a beeper/line feature test, instead of sending from the phone and screenshotting Beeper.
---

# LINE Beeper API

Drives Beeper Desktop for bridge tests, so the user only watches the LINE app.
It sends text and files into a LINE chat and reads back what Beeper got, through
Beeper Desktop's local HTTP API (`http://localhost:23373`). Run the commands
below from the repository root.

Optional. The rest of the skills work without it.

## Setup (once)

1. **Beeper Desktop is running with its API on.** `curl -s http://localhost:23373/v1/info` should
   print JSON with `"status":"running"`. If not, the user turns it on in
   Beeper Desktop under Settings > Integrations.
2. **A token for this skill.** The user creates it in Beeper Desktop: Settings >
   Integrations > "+" next to Approved connections, with read and write
   access. A separate token for this skill can be revoked on its own. The user
   saves it themselves, so it never goes through the chat:
   - macOS: `security add-generic-password -U -s beeper-desktop-api -a "$USER" -w` (prompts for it)
   - Linux: `secret-tool store --label="Beeper API" service beeper-desktop-api`
   - Anywhere: `BEEPER_ACCESS_TOKEN` in the environment

   Never ask for the token in chat, print it, or write it to a file.
3. **The allowlist.** See below. Nothing works until it has at least one chat.

## The allowlist

The script reads and sends only in the chats the user picked. The list lives
outside every repository, at `~/.config/beeper-line/allowlist.json`
(`BEEPER_LINE_ALLOWLIST` overrides it). The skill never commits it, quotes it in
a PR, or adds chats on its own.

- **First use:** when the list is empty (the script exits with code 3 and says so),
  run `discover`, show the user the LINE chat names, and ask which test chats
  to allow. `discover` lists names and types only; it reads no messages. Then
  add the user's picks with `allow add`.
- **Changes:** the user can ask at any time to add or remove chats or see the
  list (`allow add`, `allow remove`, `allow list`). Add only chats the user
  named in this conversation.
- **When a chat is refused** (exit code 3), ask the user. Never work around the list:
  don't call the API with `curl`, and don't use the Beeper MCP tools on other chats.
  The token can reach every chat, so the list only holds if the agent keeps to it.

```bash
S=.agents/skills/line-beeper-api/scripts/beeper.py
python3 $S discover [--query line]          # LINE chat names, to pick from
python3 $S allow list
python3 $S allow add <chatID|localChatID>   # only chats the user named
python3 $S allow remove <chatID|label>
```

## Commands

A chat can be given as its ID (`!…:beeper.local`), its local number, or its
label from the allowlist. When a label names more than one allowed chat, the
script refuses (exit code 3); use the chat ID.

```bash
python3 $S messages <chat> [--limit 20]                   # one line per message
python3 $S show <chat> <messageID> [--event '$id'] [--full]
python3 $S send-text <chat> "B1 hello from Beeper"
python3 $S send-file <chat> test/<slug>/samples/10-animated.gif [--type gif] [--caption ...]
python3 $S snapshot <chat> -o test/<slug>/beeper/before.json [--limit 20 | --ids a,b] [--label before]
python3 $S diff test/<slug>/beeper/before.json test/<slug>/beeper/after.json
```

- **`show`** prints the message type, send status, and per attachment
  `mimeType`, `fileName`, `fileSize`, `isGif`, `isSticker`, `isVoiceNote`,
  `duration`, `size` (width/height). It also reads Beeper's cached copy of the
  file to give `localBytes`, `sha256` and `gifFrames`: a GIF that arrives with
  one frame lost its animation.
- **`send-text` / `send-file`** wait for the message to resolve and print the same
  summary, including `sendStatus` (`SUCCESS`, `FAIL_RETRIABLE`,
  `FAIL_PERMANENT`, and the bridge's error message). `--type` is Beeper's
  attachment hint: `image`, `video`, `audio`, `file`, `gif`, `voice-note`,
  `sticker`.
- **`snapshot` / `diff`** save these summaries and compare two of them. Messages
  are matched by attachment file name, else by text. `diff` skips messages in
  the after snapshot that the before snapshot already has (same message ID),
  so an after snapshot that still reaches back into the before phase is fine. Make the
  sample file names and test strings unique (`line-test-plan` samples already
  are), and put the step ID in test texts (`B1 …`).

## In a test run

This fits the phases of `line-test-plan`:

- **before:** for each Beeper step, the user approves the step list once, then
  the agent sends with `send-text`/`send-file`, says the step ID, and the user
  only watches LINE and reports what LINE shows. After the section, `snapshot`
  to `before.json`.
- **capture:** not usable. The capture logs the bridge out (one Chrome
  Extension session at a time), so there's nothing to send through.
- **after:** the same sends on the new build, `snapshot` to `after.json`,
  then `diff`. Quote the diff in the plan's `Result` cells.

Before the first send in a session, confirm with the user: which chat, which
files, and that the bridge is logged in. Send only steps from the plan.

## Limits

- **Which side is the bridge's output.** For messages sent *from* Beeper, Beeper shows its own
  copy of what it sent. The bridge's result is on LINE, which the user
  still watches, plus the `sendStatus` the bridge reports. For messages *from*
  LINE, `show` reads exactly what the bridge produced.
- **No raw Matrix event by default.** The API returns its own message IDs, not Matrix
  event IDs. `show --event '$id'` fetches the raw event (msgtype, `info`,
  `fi.mau.gif`, thumbnail) when the user gets the event ID from Beeper
  Desktop's "View source" or from the bridge log.
- **Beeper Desktop only.** Beeper iOS and Android can render the same event
  differently. A difference there is a client bug, a separate issue; keep
  those steps manual.
- The Beeper MCP server (`claude mcp add beeper http://localhost:23373/v0/mcp -t http`)
  can send text and list messages, but it can't send files and doesn't
  check the allowlist. Use this script for tests.

Snapshots contain chat IDs, sender names and file names: keep them in
`test/<feature-slug>/beeper/`, and redact before quoting them in a PR.
