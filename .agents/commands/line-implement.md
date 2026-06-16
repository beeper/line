Implement a matrix-line feature by capturing and mimicking LINE Chrome Extension behavior.

## Input

Use the provided feature goal, for example:

```
Support Matrix group avatar changes by matching LINE Chrome Extension traffic.
```

## What to do

1. **Understand the goal and the current bridge first.**
   - Read `AGENTS.md`/`CLAUDE.md`, `docs/protocol/README.md`, relevant endpoint docs, and `docs/protocol/gap-analysis.md`.
   - Inspect the likely implementation surface in `pkg/line/`, `pkg/connector/`, and existing tests.
   - If existing protocol docs already answer the feature, implement from docs and code without a new capture.

2. **Write a capture contract before launching Chrome.**
   Capture only the missing evidence:
   - exact user-visible LINE action to perform
   - expected Talk/Auth/OBS/SSE endpoints and method names
   - request argument shape and ordering
   - response wrapper shape, headers, and error behavior
   - resulting SSE/operation events, revisions, message metadata, and local state changes
   - what values the bridge must persist or expose afterward

3. **Check LINE Chrome Extension version drift.**
   - Compare the installed/staged extension `manifest.json` version against
     `pkg/line/client.go` `ExtensionVersion`.
   - Also check literal `x-line-application` versions in `pkg/line/client.go`
     and the default `clientVersion` in `pkg/runner.go`.
   - The capture launcher prints the repo and Chrome extension versions and
     warns on mismatch. Do not silently mix capture evidence from a different
     LINE Chrome Extension version.

4. **Security preflight.**
   - Treat raw captures as secrets: they can contain access tokens, cookies, MIDs, message contents, contact data, and E2EE metadata.
   - Use a fresh `/tmp/line-chrome-extension-capture/<timestamp>-<slug>` directory with `umask 077`.
   - Do not commit raw capture files, Chrome profiles, screenshots, QR codes, or parsed JSON.
   - Do not request, store, or automate the LINE password.
   - The user performs LINE login manually, including email/password, QR, PIN, 2FA, and mobile approval.
   - Do not run the bridge and LINE Chrome Extension simultaneously unless the user explicitly accepts session invalidation.
   - Prefer the persistent capture Chrome profile from `scripts/line-chrome-capture.sh`. Use the user's normal Chrome profile only after explicit approval.

5. **Run the capture.**
   - Check for running Chrome and `matrix-line` processes; warn before closing or stopping anything.
   - Start capture:
     ```
     umask 077
     CAPTURE_DIR="/tmp/line-chrome-extension-capture/$(date +%Y%m%d-%H%M%S)-<slug>"
     bash scripts/line-chrome-capture.sh "$CAPTURE_DIR"
     ```
   - Use Codex Chrome automation when available to drive the already-logged-in extension UI. If it cannot access the capture profile or extension UI, use Computer Use.
   - The launcher stages clean unpacked LINE and Codex extension copies from the user's normal Chrome profile and auto-loads them when possible.
   - The launcher pre-skips Chrome's first-run prompts: default browser is off,
     usage/crash reporting is off, and Chrome/Sync sign-in should be skipped.
     If the sign-in screen still appears, choose "Stay Signed Out".
   - If Chrome blocks unpacked loading or LINE is missing from the capture
     profile, relaunch setup with `LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0`, open
     the official Web Store listing
     `https://chromewebstore.google.com/detail/line/ophjlpahpchlmihnnnihgmmeilfjmjjc`,
     verify LINE / LY Corporation, click Add to Chrome, and accept the standard
     "Add extension" confirmation when the user has requested setup.
   - If Chrome is not logged in, pause and ask the user to complete LINE login manually in the capture browser.
   - Perform the smallest LINE UI flow that satisfies the capture contract, then quit Chrome completely.

6. **Parse, redact, and analyze.**
   - Prefer CDP output because it includes SSE messages:
     ```
     python3 scripts/parse-line-traffic.py "$CAPTURE_DIR/line-cdp-capture.json" "$CAPTURE_DIR/line-cdp-capture-parsed.json"
     python3 scripts/redact-line-capture.py "$CAPTURE_DIR/line-cdp-capture-parsed.json"
     ```
   - Read raw files only locally and only when redacted output loses information needed for implementation.
   - Quote or document only redacted snippets.
   - Compare against `pkg/line/methods.go`, `pkg/line/client.go`, `pkg/line/sse.go`, and the relevant `pkg/connector/` flow.
   - Produce an evidence table: Chrome behavior, bridge behavior, implementation delta, files to change.

7. **Implement religiously.**
   - Match the Chrome Extension's endpoint path, service/method name, argument order, headers, request body shape, response handling, and fallback behavior.
   - Preserve existing bridge architecture: Thrift/API code in `pkg/line`, Matrix-facing behavior in `pkg/connector`, E2EE behavior in `pkg/e2ee`/`pkg/ltsm`.
   - Update protocol docs only with redacted request/response/event evidence.
   - Add or update focused tests for parsing, request construction, event handling, persistence, and fallback/error behavior.

8. **Verify and clean up.**
   - Run:
     ```
     go fmt ./...
     goimports -local "github.com/highesttt/matrix-line-messenger" -w .
     go test ./...
     go vet $(go list ./... | grep -v /ltsm)
     ```
   - If available, run `staticcheck $(go list ./... | grep -v /ltsm)`.
   - Before finishing, report where raw captures live and ask whether to delete them. Prefer keeping only redacted notes/docs.
