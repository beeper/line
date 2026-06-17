---
name: line-implement
description: Plans and implements matrix-line bridge features by inspecting bridge code, deriving the missing LINE Chrome Extension evidence, securely capturing/analyzing traffic, and coding behavior to match Chrome. Use when implementing LINE protocol features, mimicking LINE Chrome Extension APIs, or investigating matrix-line behavior from network captures.
---

# Line Implement

## Quick Start

Use this for the Matrix LINE bridge when a feature goal depends on exact LINE
Chrome Extension behavior.

1. Confirm the repo is `beeper-line`/`matrix-line-messenger`; read `AGENTS.md`.
2. Treat `.agents` as the repo-local source of truth. Read
   `.agents/commands/line-implement.md` if present. `.claude/commands` may exist
   only as a compatibility symlink to `.agents/commands`.
3. Load [REFERENCE.md](REFERENCE.md) for the full workflow.
4. Check LINE Chrome Extension version drift before capture: compare the repo
   `pkg/line/client.go` `ExtensionVersion` and hardcoded LINE application
   header versions against the installed extension `manifest.json`.
5. First inspect code and `docs/protocol`; capture only unknown behavior.
6. Treat all raw captures as secrets and work from redacted outputs whenever possible.

## Required Loop

1. Convert the user's feature goal into a capture contract.
2. Compare existing protocol docs and bridge implementation.
3. Use secure Chrome/CDP capture only for missing evidence.
4. Parse and redact captures before quoting, documenting, or sharing details.
5. Implement the bridge to match Chrome's endpoint, headers, argument shape,
   response handling, events, and fallback behavior.
6. Run focused tests plus repo formatting/linting commands.

## Automation

For Chrome UI work, prefer the Codex Chrome connector when it can access the
needed browser/profile. If it cannot operate the LINE extension UI, use
Computer Use.

The capture launcher should pre-skip Chrome first-run prompts. Keep the default
browser checkbox off, usage/crash reporting off, and Chrome/Sync signed out. If
Chrome still shows the sign-in page, choose "Stay Signed Out"; only LINE login is
manual.

Before trusting a capture, check the installed LINE Chrome Extension version
against the repo version. The repo source is `pkg/line/client.go`
`ExtensionVersion`; also scan for literal `x-line-application` versions in
`pkg/line/client.go` and the default client version in `pkg/runner.go`. The
installed extension version comes from
`~/.cache/line-chrome-extension-capture/chrome-debug-profile/Default/Extensions/ophjlpahpchlmihnnnihgmmeilfjmjjc/*/manifest.json`
or the staged extension manifest. If they differ, warn the user and either
update the repo version constants/headers or note that the evidence is from a
different Chrome Extension version.

The capture launcher stages clean unpacked LINE/Codex extension copies from the
user's normal Chrome profile before loading them, because Chrome Web Store
install directories contain metadata that unpacked-extension loading rejects.
If LINE is already installed in the persistent capture profile, the launcher
uses that installed copy instead of injecting a staged LINE copy.
When installing LINE from the Chrome Web Store into the persistent capture
profile, run the launcher with `LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0` so the
Web Store is not fighting an already command-line-loaded extension ID.

If the LINE extension is missing from the capture Chrome profile and the user
has asked to set it up, open the official Chrome Web Store listing for extension
ID `ophjlpahpchlmihnnnihgmmeilfjmjjc` (LINE, offered by LY Corporation), click
Add to Chrome, and accept the standard "Add extension" confirmation. Do not
accept prompts for any other extension or publisher without a new user request.

Do not request, store, or automate LINE credentials. If the extension is not
logged in, pause and ask the user to complete login manually in the capture
browser, including email/password, QR, PIN, 2FA, and mobile approval.
