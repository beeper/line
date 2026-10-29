# Feature parity with the previous development checkpoint

Comparison baseline: checkpoint `910b4b3dceceb1dcd0e0b34e805ffc83d13d594f`
and known working tree `4eec1754b7916cbeed622529e5c9fb9ca129f6d2`, compared
with upstream-first release source `e50154bda1f0ac2632574048760d1bee18e5331a`.
The new source is a separate upstream integration history, so ancestry alone is
not evidence of parity. Feature and test paths were reviewed directly.

| Historical feature | Source | Current implementation | Status / action | Coverage |
| --- | --- | --- | --- | --- |
| Friendly `login qr`, `login email`, `login help` | `4e9b184` | `pkg/connector/login_command.go`; registered in `LineConnector.Init` | Missing in rebuild because custom handler registration was omitted; restored | `pkg/connector/login_command_test.go` uses real framework processor |
| QR auth lifecycle and flow ID | upstream PR #271; `09a8685`, `6e333e5`, `4eec175` | `connector.go`, `login_qr.go`, `pkg/line/qr.go` | Present; alias dispatch is separate | `login_qr_test.go`, `qr_failure_test.go`, `qr_error_test.go` |
| Sanitized provider auth errors and cooldown | `6e333e5`, `596334a`, `4eec175`, `c6cf461`, `c1d2fad`, `7c4303c` | `loginerrors.go`, `login_cooldown.go`, `pkg/line/auth_error.go`, `pkg/line/errors.go` | Present and extended; do not roll back | `login_cooldown_test.go`, `login_error_logging_test.go`, `auth_error_test.go` |
| Public LINE ID resolution before portal creation | `695d6d9` | `userinfo.go`, `resolve_identifier.go` | Present | `resolve_identifier_test.go`, `identity_audit_test.go` |
| MID normalization and opaque case-sensitive IDs | `7615af2`, `05a1607`, `bde3468` | `pkg/line/user_mid.go`, `resolve_identifier.go`, `userinfo.go` | Present, with deliberate preservation of gateway identity case | `user_mid_test.go`, `opaque_mid_test.go`, `resolve_identifier_test.go` |
| Contact lookup validation / empty query safety | `160ac20`, `46a2e79` | `userinfo.go` | Present; validates returned contacts and avoids empty-query enumeration | `resolve_identifier_test.go` |
| Optional sanitized lookup-shape diagnostics | `65a2c02` | No current diagnostics code/config | Removed/superseded from rebuild. Not restored: diagnostics were opt-in but the old response parser/key allowlist is not present for current protocol shapes; requires a fresh privacy review and tests before reintroduction | Historical tests existed; no current coverage |
| Portal creation, canonical reuse, reopen and cancellation | `695d6d9`, `15ff9e4`, `971a828` | `resolve_identifier.go`, bridgev2 portal flow | Present | `resolve_identifier_test.go`, `identity_audit_test.go` |
| Outbound delivery, cancellation, provider error status | `c4ffe53`, `0597689`, `72c7a51` | `send_message.go`, `senderrors.go`, connector status mapping | Present | `send_message_test.go`, `outbound_pipeline_test.go` |
| Inbound handling and sync diagnostics | `022678d`, `d57862a` | `handle_message.go`, `sync.go` and bridge structured logs | Present; current upstream path retained | `handle_message_test.go`, `sync_test.go` |
| Media auth, image metadata, HEIC, audio, stickers, link previews | `0b79d0f`, `17ae6dc`, `e2670d4`, `6f7f92b`, `055d62e`, upstream PR #286 | `media.go`, `pkg/line/publicmedia.go`, handlers | Present in current rebuild | media and handler tests including `media_test.go`, `image_media_test.go`, `text_test.go` |
| Group E2EE spec version / crypto runtime behavior | `c5a432d`, `9be42f2`, `8811f31`, `b68db47` | `e2ee_keys.go`, `pkg/e2ee`, `pkg/ltsm` | Present/current implementation retained | `e2ee_keys_test.go`, `manager_test.go` |
| Test optimization and coverage gates | test optimization PR #1; `a8d80f2`, `1b0db13` | `scripts/test.sh`, `scripts/check_test_coverage.py`, coverage workflow | Present | normal/race/coverage/release modes and coverage gate |
| Build/release reproducibility and native packaging | `ffb62b5`, `e50154b` | `build.sh`, release scripts/docs | Present; exact release artifacts preserved | release validation workflow |

## Intentional differences

- The mautrix-go framework still accepts fully qualified login flow IDs; the
  LINE aliases wrap, rather than replace, its standard `login` handler and keep
  the framework's permissions, login limits, relogin, and command-state logic.
- QR login remains controlled by `network.qr_login`. The friendly help omits QR
  when that option is disabled.
- Historical lookup diagnostics are not blindly restored. Current identity
  validation and safe errors remain; any new diagnostics need current-schema
  allowlisting and a privacy-focused review.
- No production deployment or database/session changes are part of this work.

## Remaining gaps

- The historical lookup-diagnostics feature needs a fresh implementation if
  operational evidence shows it is still needed.
- Live Matrix/LINE behavior must be validated after an operator deploys the
  prepared release candidate; this source change does not deploy it.
