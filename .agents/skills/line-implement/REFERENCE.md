# Line Implement Reference

## Repo Orientation

Start with:

- `AGENTS.md` / `CLAUDE.md`
- `.agents/commands/line-capture.md`
- `.agents/commands/line-analyze.md`
- `.agents/commands/line-implement.md`
- `docs/protocol/README.md`
- `docs/protocol/endpoints/README.md`
- `docs/protocol/gap-analysis.md`
- `pkg/line/methods.go`, `pkg/line/client.go`, `pkg/line/sse.go`
- likely `pkg/connector/*` files for the Matrix-facing feature

Do not capture if the current protocol docs already provide enough evidence.
Use `.agents` as the repo-local source of truth. `.claude/commands` is a
compatibility symlink for Claude-style command discovery when present.

## Capture Contract

Before launching Chrome, write down:

- the exact LINE UI action to perform
- fixture requirements such as DM/group, `c...`/`r...` chat MID type, media type,
  old/new state, or Letter Sealing state
- expected endpoint families: TalkService, AuthService, LoginQrCode, OBS, SSE,
  CDN, or secondary APIs
- unknowns needed for implementation: method name, URL, argument order, request
  body shape, response wrapper, headers, operation events, revision changes,
  persisted IDs, and fallback/error behavior

Keep the UI flow narrow. Avoid broad exploratory captures unless the protocol
surface is genuinely unknown.

## Version Preflight

Before trusting capture evidence, compare the repo's LINE Chrome Extension
version with the installed extension:

```bash
rg -n 'ExtensionVersion|x-line-application|clientVersion' pkg/line/client.go pkg/runner.go
find "$HOME/.cache/line-chrome-extension-capture/chrome-debug-profile/Default/Extensions/ophjlpahpchlmihnnnihgmmeilfjmjjc" \
  -maxdepth 2 -type f -name manifest.json -print
```

The canonical repo version is `pkg/line/client.go` `ExtensionVersion`. Also
check hardcoded `x-line-application` header strings in `pkg/line/client.go` and
the default `clientVersion` in `pkg/runner.go`; those must not drift from the
extension manifest version. The capture launcher prints repo and Chrome LINE
versions at startup and warns on mismatch. If versions differ, do not silently
mix evidence: warn the user and either update the repo version constants/headers
or record that the capture was taken from a different LINE Chrome Extension
version.

## Security Rules

Raw LINE captures are secrets. They may include access tokens, cookies, session
IDs, MIDs, contacts, message bodies, media object IDs, QR/login artifacts, and
E2EE metadata.

- Use `/tmp/line-chrome-extension-capture/<timestamp>-<slug>` with `umask 077`.
- Prefer the persistent dedicated Chrome profile created by `scripts/line-chrome-capture.sh`
  at `~/.cache/line-chrome-extension-capture/chrome-debug-profile`.
- Ask before using the user's normal Chrome profile.
- Ask before stopping a running bridge or invalidating an active LINE session.
- Keep remote debugging local and close Chrome after capture.
- If the user asks to set up the capture browser, installing the official LINE
  Chrome Extension is in scope. Verify extension ID
  `ophjlpahpchlmihnnnihgmmeilfjmjjc`, listing name LINE, and publisher/offered
  by LY Corporation before clicking Add to Chrome and accepting the standard
  "Add extension" confirmation.
- Do not request, store, or automate LINE credentials.
- Never ask the user to paste their LINE password into chat, command arguments,
  files, or terminal output.
- If the capture browser is not logged in, pause and ask the user to complete
  LINE login manually in Chrome, including email/password, QR, PIN, 2FA, and
  mobile approval.
- Never commit raw captures, Chrome profiles, screenshots, QR codes, parsed JSON,
  or redacted files unless the user explicitly asks for sanitized docs.
- Quote/document only redacted snippets.

## Capture Commands

Use CDP capture first because it includes SSE event bodies:

```bash
umask 077
CAPTURE_DIR="/tmp/line-chrome-extension-capture/$(date +%Y%m%d-%H%M%S)-feature-slug"
bash scripts/line-chrome-capture.sh "$CAPTURE_DIR"
```

The launcher stages clean unpacked copies of the LINE and Codex Chrome extension
directories from the user's normal Chrome profile when they are installed there,
then auto-loads those staged copies. When LINE is already installed in the
persistent capture profile, the launcher uses that installed copy and only
auto-loads other staged extensions; in that mode it must not pass
`--disable-extensions-except` for only the staged helpers, because that disables
the installed LINE extension. Override detection with
`LINE_CHROME_EXTENSION_DIR` or `CODEX_CHROME_EXTENSION_DIR` when needed.
If unpacked loading is blocked by Chrome, use the persistent capture profile to
open the official LINE Chrome Web Store listing and install it there; start this
setup run with `LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0` so Chrome is not also
loading a staged unpacked extension with the same ID. Accept the expected
extension confirmation only after the user has requested setup.
It also pre-seeds the dedicated capture profile so Chrome first-run/sign-in
prompts do not interrupt capture: default-browser opt-in is off, usage/crash
reporting is off, and Chrome/Sync stays signed out. If Chrome still shows the
sign-in screen, choose "Stay Signed Out" and continue to LINE login only.

When Chrome closes:

```bash
python3 scripts/parse-line-traffic.py "$CAPTURE_DIR/line-cdp-capture.json" "$CAPTURE_DIR/line-cdp-capture-parsed.json"
python3 scripts/redact-line-capture.py "$CAPTURE_DIR/line-cdp-capture-parsed.json"
```

Use the net-log only as a backup:

```bash
python3 scripts/parse-line-traffic.py "$CAPTURE_DIR/line-net-log.json" "$CAPTURE_DIR/line-net-log-parsed.json"
python3 scripts/redact-line-capture.py "$CAPTURE_DIR/line-net-log-parsed.json"
```

## Analysis Checklist

Build an evidence table with:

- ordered requests and timestamps
- endpoint URL and service/method
- meaningful headers such as `x-lhm`, `x-lpv`, `x-lsr`, OBS headers, and content type
- request argument shape and exact order
- response status, wrapper code/message/data, and important headers
- SSE/operation event type, params, revisions, message metadata, LOC_KEYs, and
  localRev/chat revision changes
- Chrome behavior vs bridge behavior
- implementation delta and files to change

For Thrift payloads, prefer structured decoded data when available. If only
opaque binary/base64 is available, use the URL, service/method, headers, and
observable response/event behavior, then map to existing Go request builders.

## Implementation Rules

- Put LINE API/client behavior in `pkg/line`.
- Put Matrix bridge behavior and portal/message handling in `pkg/connector`.
- Keep E2EE behavior in `pkg/e2ee` and avoid editing generated `pkg/ltsm/wbc_generated.go`.
- Match Chrome's method names, endpoint paths, arg order, header semantics,
  body shape, retry/fallback behavior, and event interpretation.
- Prefer existing request helpers and local patterns over new abstractions.
- Add protocol docs for new endpoint behavior using only redacted evidence.
- Add focused tests for request construction, response parsing, event handling,
  persistence, and user-visible bridge behavior.

## Verification

Run the relevant subset, then the broader checks when practical:

```bash
go fmt ./...
goimports -local "github.com/highesttt/matrix-line-messenger" -w .
go test ./...
go vet $(go list ./... | grep -v /ltsm)
staticcheck $(go list ./... | grep -v /ltsm)
```

If a command is unavailable or too slow, report that clearly.

At the end, state where raw captures remain and ask whether to delete them.
