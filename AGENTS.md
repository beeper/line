# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Matrix bridge for LINE Messenger using mautrix-go (bridgev2). It bridges messages between Matrix clients and LINE, supporting both Letter Sealing ON (E2EE) and OFF accounts.

The bridge identifies as a LINE Chrome Extension client, so it cannot coexist with an actual Chrome Extension session.

## Build & Development

**Requirements:** Go 1.26+, libolm (`libolm-dev` on Ubuntu, `olm` via Homebrew on macOS)

**Build:**
```bash
./build.sh
```

**Run:**
```bash
cd data && ../matrix-line
```

**Docker:**
```bash
docker compose up --build
```

**Formatting/Linting (via pre-commit):**
```bash
go fmt ./...
goimports -local "github.com/highesttt/matrix-line-messenger" -w .
staticcheck $(go list ./... | grep -v /ltsm)
go vet $(go list ./... | grep -v /ltsm)
```

Note: `staticcheck` and `go vet` exclude the `pkg/ltsm` package (transpiled WASM code). Imports must use `-local` flag to group project-local imports correctly.

## Architecture

```
Matrix Client <-> mautrix bridgev2 framework <-> LineConnector/LineClient <-> LINE API
```

### Key Packages

- **`cmd/matrix-line/`** — Entry point, uses `mautrix.BridgeMain`
- **`pkg/connector/`** — Bridge logic implementing `bridgev2.NetworkConnector` and `bridgev2.NetworkAPI`
  - `connector.go` — `LineConnector`: bridge initialization, login flow management
  - `client.go` — `LineClient`: token management, polling, message routing
  - `handle_message.go` / `send_message.go` — Inbound/outbound message conversion
  - `e2ee_keys.go` — Peer key negotiation and group key fetching
  - `media.go` — Media upload/download with E2EE support
  - `sync.go` — Chat prefetching and long-poll event loop
- **`pkg/line/`** — HTTP client for LINE's Thrift-based API
  - `client.go` / `methods.go` — API calls (login, messaging, contacts, groups)
  - `sse.go` — Server-sent events for long-polling
  - `password/` — RSA password encryption for login
  - `secret/` — E2EE secret generation for login handshake
- **`pkg/e2ee/`** — E2EE encryption/decryption manager wrapping the LTSM runtime
- **`pkg/ltsm/`** — Transpiled WASM module for LINE's white-box crypto (do not edit `wbc_generated.go`)

### E2EE Design

Two login paths exist:
- **LSON (type 2):** Full E2EE — generates keypair, exchanges encrypted keychain, enables Curve25519-based message encryption
- **LSOFF (type 0):** Fallback when LINE error 89 indicates E2EE not supported

Message encryption is hybrid:
- Pure Go Curve25519 for known key material
- WASM-transpiled white-box crypto for SKB-wrapped keys
- V1 (AES-256-CBC + MAC) and V2 encryption schemes
- Graceful fallback to plaintext when peer/group doesn't support E2EE

### Token Management

`LineClient` implements proactive token refresh based on server-provided duration, with automatic recovery on expiry (refresh token -> re-authentication). Token expiry is detected via LINE error codes 119 and 10051.

## Logging

Uses `zerolog`. Pre-commit hooks enforce: no `Msgf` (use `Msg` with structured fields), use `Stringer` interface where applicable.
