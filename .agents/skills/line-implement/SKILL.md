---
name: line-implement
description: Plans and implements beeper/line (matrix-line) bridge features by inspecting the bridge code, deriving the missing LINE Chrome Extension evidence, securely capturing and analyzing traffic, and coding the behavior to match Chrome. Use when implementing LINE protocol features, mimicking LINE Chrome Extension APIs, or investigating bridge behavior from network captures.
---

# LINE Implement

Implements a bridge feature by capturing and mimicking the LINE Chrome Extension.
It is the umbrella over two sibling skills: `line-capture` (record traffic) and
`line-analyze` (parse, redact, compare), in `.agents/skills/line-capture/` and
`.agents/skills/line-analyze/`. Run the commands below from the repository
root. Load [REFERENCE.md](REFERENCE.md) for the long version of each step.

## Input

A feature goal, for example: `Support Matrix group avatar changes by matching LINE Chrome Extension traffic.`

## Workflow

0. **Write the test plan first (optional).** The `line-test-plan` skill turns the goal into a numbered `test/<feature>/PLAN.md` (sections, step IDs, before/capture/after phases, Result column) before any capture or code. The capture contract below can then point at its step IDs.

1. **Understand the goal and the current bridge first.**
   - Read `AGENTS.md`/`CLAUDE.md`, `docs/protocol/README.md`, relevant endpoint docs, and `docs/protocol/gap-analysis.md`.
   - Inspect the likely implementation surface in `pkg/line/`, `pkg/connector/`, and existing tests.
   - If existing protocol docs already answer the feature, implement from docs and code without a new capture.

2. **Write a capture contract before launching Chrome.** Capture only the missing evidence:
   - exact user-visible LINE action to perform
   - expected Talk/Auth/OBS/SSE endpoints and method names
   - request argument shape and ordering
   - response wrapper shape, headers, and error behavior
   - resulting SSE/operation events, revisions, message metadata, and local state changes
   - what values the bridge must persist or expose afterward

3. **Check LINE Chrome Extension version drift.**
   - Compare the installed/staged extension `manifest.json` version against `pkg/line/client.go` `ExtensionVersion`.
   - Also check literal `x-line-application` versions in `pkg/line/client.go` and the default `clientVersion` in `pkg/runner.go`.
   - The capture launcher prints the repo and Chrome extension versions and warns on mismatch. Do not silently mix capture evidence from a different LINE Chrome Extension version.

4. **Security preflight.**
   - Treat raw captures as secrets: they can contain access tokens, cookies, MIDs, message contents, contact data, and E2EE metadata.
   - Use a fresh `/tmp/line-chrome-extension-capture/<timestamp>-<slug>` directory with `umask 077`.
   - Do not commit raw capture files, Chrome profiles, screenshots, QR codes, or parsed JSON.
   - Do not request, store, or automate the LINE password.
   - Do not open DevTools on the LINE extension during a capture (LINE has `debugger;` traps); the recorder does not need it. The launcher opens `about:blank`; the user opens LINE from the extension's toolbar icon.
   - The user performs LINE login manually, including email/password, QR, PIN, 2FA, and mobile approval.
   - Do not run the bridge and LINE Chrome Extension simultaneously unless the user explicitly accepts session invalidation.
   - Prefer the persistent capture Chrome profile that the `line-capture` launcher creates. Use the user's normal Chrome profile only after explicit approval.

5. **Run the capture** with the `line-capture` skill:
   - Run its preflight (`bash .agents/skills/line-capture/scripts/line-capture-preflight.sh`; Git Bash on Windows). If anything is `MISSING`, pick install commands that fit the reported OS and the package managers present, show them to the user, install only after they confirm, and re-run until it passes.
   - Check for running Chrome and `matrix-line` processes; warn before closing or stopping anything.
   - Start the capture from the checkout:
     ```bash
     umask 077
     CAPTURE_DIR="/tmp/line-chrome-extension-capture/$(date +%Y%m%d-%H%M%S)-<slug>"
     bash .agents/skills/line-capture/scripts/line-chrome-capture.sh "$CAPTURE_DIR"
     ```
   - Use a Chrome automation tool when available to drive the already-logged-in extension UI. If it cannot access the capture profile or extension UI, use computer use.
   - If Chrome blocks unpacked loading or LINE is missing from the capture profile, set up the extension as `line-capture` describes (`LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0`, official Web Store listing `https://chromewebstore.google.com/detail/line/ophjlpahpchlmihnnnihgmmeilfjmjjc`, verify LINE / LY Corporation) only when the user has requested setup.
   - If Chrome is not logged in, pause and ask the user to complete LINE login manually in the capture browser.
   - Perform the smallest LINE UI flow that satisfies the capture contract, then quit Chrome completely.

6. **Parse, redact, and analyze** with the `line-analyze` skill. Prefer the CDP output because it includes SSE messages:
   ```bash
   python3 .agents/skills/line-analyze/scripts/parse-line-traffic.py "$CAPTURE_DIR/line-cdp-capture.json" "$CAPTURE_DIR/line-cdp-capture-parsed.json"
   python3 .agents/skills/line-analyze/scripts/redact-line-capture.py "$CAPTURE_DIR/line-cdp-capture-parsed.json"
   ```
   - Read raw files only locally and only when redacted output loses information needed for implementation.
   - Quote or document only redacted snippets.
   - Compare against `pkg/line/methods.go`, `pkg/line/client.go`, `pkg/line/sse.go`, and the relevant `pkg/connector/` flow.
   - Produce an evidence table: Chrome behavior, bridge behavior, implementation delta, files to change.

7. **Implement religiously.**
   - Match the Chrome Extension's endpoint path, service/method name, argument order, headers, request body shape, response handling, and fallback behavior.
   - Preserve the bridge architecture: Thrift/API code in `pkg/line`, Matrix-facing behavior in `pkg/connector`, E2EE behavior in `pkg/e2ee`/`pkg/ltsm`.
   - Update protocol docs only with redacted request/response/event evidence.
   - Add or update focused tests for parsing, request construction, event handling, persistence, and fallback/error behavior.

8. **Verify and clean up.**
   - Run the commands from the repo's `AGENTS.md` (typically):
     ```bash
     go fmt ./...
     goimports -local "github.com/beeper/line" -w .
     go test ./...
     go vet $(go list ./... | grep -v /ltsm)
     ```
   - If available, run `staticcheck $(go list ./... | grep -v /ltsm)`.
   - Before finishing, report where raw captures live and ask whether to delete them. Prefer keeping only redacted notes/docs.

## Evidence

After testing the feature, the `line-evidence` skill turns the screenshots into
PR evidence: it collects them, lets the user pick from a local contact sheet,
covers everything that is not test data, and writes approved PNGs and a manifest
to `test/<feature>/evidence/`. Raw screenshots never leave the machine.

## Automation notes

- The capture launcher pre-skips Chrome's first-run prompts. Keep the default browser checkbox off, usage/crash reporting off, and Chrome/Sync signed out. If the sign-in page still shows, choose "Stay Signed Out"; only LINE login is manual.
- The recorder stores request bodies (binary uploads as size + sha256, plus base64 in the raw capture only) and the real on-the-wire request headers. Redact before quoting.
- Do not accept Chrome prompts for any other extension or publisher without a new user request.
