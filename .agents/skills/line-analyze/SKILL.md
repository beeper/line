---
name: line-analyze
description: Parses and redacts a LINE Chrome Extension network capture (CDP capture or Chrome net-log) and compares it with the beeper/line bridge code. Use after a line-capture run, or when given a LINE net-log or CDP capture file to analyze.
---

# LINE Analyze

Turns a raw LINE Chrome Extension capture into redacted, readable findings and
compares them with the bridge. Run the commands below from the repository root.

## Input

Use the provided path to a capture file. Prefer the CDP capture
(`line-cdp-capture.json`): it includes SSE messages and request bodies. The
Chrome net-log is a backup. If no path is provided, default to
`/tmp/line-chrome-extension-capture/line-cdp-capture.json`, then
`/tmp/line-chrome-extension-capture/line-net-log.json`.

## What to do

1. Run the parser. This extracts LINE-related HTTP requests and writes a
   `-parsed.json` file:

   ```bash
   python3 .agents/skills/line-analyze/scripts/parse-line-traffic.py <capture-file> [<parsed-output.json>]
   ```

2. Redact it before reading or quoting anything. This removes tokens, MIDs,
   `picturePath` and raw body bytes:

   ```bash
   python3 .agents/skills/line-analyze/scripts/redact-line-capture.py <parsed-output.json>
   ```

   Read the raw files only locally, and only when the redacted output loses
   information you need. Quote or document only redacted snippets.

3. Read the redacted output and present findings by category:

   - **Thrift RPC** (`/api/talk/thrift/`): The main LINE API. Requests use LINE's Thrift-over-HTTP protocol. The `x-lhm` header indicates the Thrift method name. Compare against methods in `pkg/line/methods.go`.
   - **Long-poll / SSE** (`/api/talk/long-polling/`, `/api/operation/receive`): Real-time event delivery. LF1 is used during login verification, JQ for login polling, and SSE for ongoing message/operation streaming. Compare against `pkg/line/sse.go` and `pkg/line/client.go`.
   - **Auth** (`/api/auth/`): Token refresh and login flows. Compare against login methods in `pkg/line/client.go` (loginV2, confirmE2EELogin, tokenRefresh).
   - **Media/OBS** (`obs.line-apps.com`): Media upload/download. Check `x-obs-params` header for operation details, `x-obs-oid` in responses. Compare against `pkg/line/client.go` (UploadOBS, DownloadOBS).
   - **Stickers/CDN** (`stickershop`, `scdn`): Sticker image downloads.
   - **Secondary API** (`legy-jp.line-apps.com`): Link previews and page info. Compare against `pkg/line/client.go` (GetPageInfo).

4. For each interesting request, explain:
   - What the bridge equivalent is (or note if it's not implemented yet)
   - The request/response structure
   - Any headers that carry protocol-specific meaning

5. Highlight any **unimplemented** endpoints or behaviors that differ from what
   the bridge currently does. Cross-reference with `pkg/line/methods.go` and
   `pkg/connector/`.

6. After presenting the analysis, ask the user if they want to clean up the
   capture files (`/tmp/line-chrome-extension-capture/`). Warn that the raw
   capture and parsed output contain sensitive data (auth tokens in
   `X-Line-Access` headers, message IDs, etc.) and must not be left on disk or
   committed to a repository. If the user agrees, delete the entire capture
   directory.

## LINE protocol context

- The bridge identifies as LINE Chrome Extension: header `x-line-application` contains the app version string.
- Thrift RPC uses HTTP POST to `line-chrome-gw.line-apps.com/api/talk/thrift/Talk/<Service>/<Method>`.
- Auth token is in `x-line-access` header.
- E2EE messages have `chunks` field containing encrypted content (base64 segments).
- Operations have numeric `type` codes. See `pkg/connector/sync.go` and `pkg/connector/handle_message.go` for the operation type switch.
- Body content in the parsed output may be base64-encoded binary (Thrift serialization). Note the method name from headers rather than trying to decode the binary body.
