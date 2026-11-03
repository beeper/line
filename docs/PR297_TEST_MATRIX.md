# PR #297: reproducible test matrix and evidence

See [before/after](PR297_BEFORE_AFTER.md) for the comparison boundary and
[blast radius](PR297_BLAST_RADIUS.md) for risks/rollback. This is a replay guide,
not a claim that mocked tests validate LINE's production behavior.

## Evidence and statuses

- **AUTOMATED PASS**: local fixture/mock execution, no live LINE services.
- **LIVE VERIFIED**: a specific observed live result with attributable source.
- **MANUAL NOT RUN**: no attributable per-scenario live evidence.
- **NOT TESTABLE WITHOUT PROVIDER CONDITIONS**: do not provoke a restriction.
- **KNOWN LIMITATION**: no claim of a fix/guarantee.

### What was actually run

| Evidence | Source / environment | Result and boundary |
| --- | --- | --- |
| Historical full normal/race/coverage/release | Integrated runtime commit `07badf92460e5ae744b90d1c0c0a46cc30396218`; Go 1.26.10, Linux amd64, CGO/libolm 3.2.16 | Each suite: 512 pass test/subtest events, 1 skip, 0 failures. Release repeats all three plus format/vet/staticcheck/module verification/build/coverage-checker tests. All package floors passed. Source diff from this commit through pre-documentation head `81f502150b88e681d2703239df608b1b38ccc2d6` is only `docs/UPSTREAM_INTEGRATION_STATUS.md`; runtime/tests/CI are identical. This is reused evidence, not a newly run exact-head full suite. |
| Fresh targeted replay, 2026-11-02 | `81f502150b88e681d2703239df608b1b38ccc2d6`, before this documentation-only change; same local Go/libolm setup | 146 pass test/subtest events, 0 skips/failures. `fast` also ran gofmt/vet/staticcheck on the three selected packages. Exact command below. |
| Historical skip | `pkg/runner_vectors_replay_test.go`: `TestReplayLTSMVectors` | Explicit existing skip: old JSON IPC replay needs rewriting for the typed/wazero runner. Supplying vectors does not enable this test. This is not a blanket skip of crypto/stress tests. |
| Checkpoint build | `f36c646b468719877c9fb4329f2cab18b652fbca`, v1.2.0+dev.f36c646b | Two same-environment, fixed-epoch builds were identical. Tagged artifact SHA256 `dabf2c816b752341328411ab02fa73d756c20b82097abab38a46e38916649c81`; not a build of today's upstream merge. |
| Historical production startup | Same `f36c646b` version; working binary SHA256 `a8483a0342fc9b0715ec087a6c773a0b40a8b63c122895cc714940c9bee7f4b8` | **LIVE VERIFIED** startup/service active, DB schemas current, Matrix appservice connectivity. This binary predates the checkpoint tag: same source/time/toolchain but empty embedded tag, hence different checksum. Not evidence for every login/media scenario or the PR's later upstream integration. |
| Author's live report | [PR comment](https://github.com/beeper/line/pull/297#issuecomment-6546539362) reports use over multiple days | Useful operational feedback, but no per-scenario/source inventory. Version attribution for individual scenarios is uncertain; do not label them LIVE VERIFIED. |
| GitHub CI | No checks reported when inspected on 2026-11-02 | Not a green CI claim; PR conflicts with newer main. No tests in this guide validate the unresolved combined tree. |

### Automated replay (no LINE credentials needed)

Use Go 1.26+, libolm development headers, Python 3, `goimports`, and `staticcheck`
on PATH. Run in a checkout of **this PR branch**, not the production data folder.
Record `git rev-parse HEAD`, `go version`, and `pkg-config --modversion olm`.
Package/event counts include nested subtests, not 512 independent user journeys.

```sh
TEST_JSON=1 TEST_RUN='Test(RegisteredLineLoginCommandDispatch|ResolveIdentifier|StartChat|OpaqueMID|LookupToOutbound|QR|LoginCooldown|LoginClaim|LoginRateLimit|EmailLoginRate|StoredCredentialRecovery|VerificationTemporarily|CanceledEmail|SubmitUserInputLogs|SessionEstablishment|ReconnectError|SanitizeProvider|ParseRetryAfter|HTTPErrorPreserves|Outbound|SendMessage|MediaHMAC|LineImage|ConvertTextUploads)' \
  bash scripts/test.sh fast ./pkg/connector ./pkg/line ./pkg/connector/handlers

# Full validation, optional for reviewers but required after integrating conflicts:
bash scripts/test.sh normal
bash scripts/test.sh race
bash scripts/test.sh coverage
bash scripts/test.sh release
```

`release` reruns normal/race/coverage and builds a local binary; it never deploys.
Use `TEST_PACKAGE_PARALLELISM=2`, `GOMAXPROCS=4` (defaults) for bounded load.
Coverage floors stay enabled. CI matrix runs normal/race/coverage, not `release`.

## Automated implementation-to-test map

Paths are relative to repository root. All tests below use synthetic fixtures,
mock transports/Matrix intents and (where needed) temporary SQLite databases;
none logs in to a real LINE account. HMAC/crypto tests execute local crypto.

| Source path / behavior | Test file and function | Key assertion / method |
| --- | --- | --- |
| `pkg/connector/login_command.go`, `connector.go`: registration and selection | [login_command_test.go](../pkg/connector/login_command_test.go): `TestRegisteredLineLoginCommandDispatch` | Real mautrix-go Processor initialized by bridge; help, aliases/case, full IDs, unknown/disabled flow; mock `CreateLogin` stops before auth. Checks framework login-permission requirement retained, not a comprehensive permissions suite. |
| `pkg/connector/login_qr.go`: lifecycle | [login_qr_test.go](../pkg/connector/login_qr_test.go): `TestQRConcurrentWaitIsRejected`, `TestQRApprovalPINAndTokenValidation`, `TestQRExpiryAndCancellation`, `TestQRStartPreservesDeadline`, `TestQRPasswordlessReconnectNeedsApproval` | Mock scan/PIN/token responses, reject incomplete finalization, concurrent wait and expired/canceled attempts; fresh approval required for passwordless reconnect. |
| `pkg/line/qr.go`, `auth_error.go`: request/stage/provider errors | [qr_contract_test.go](../pkg/line/qr_contract_test.go): `TestQRRequestContractAndMIDRepresentations`; [qr_error_test.go](../pkg/line/qr_error_test.go): `TestQRErrorDetailsAndRedaction`, `TestQRAuthErrorStage`, `TestQRRPCErrorAuthError`, `TestParseRetryAfter`, `TestHTTPErrorPreservesRetryAfter` | HTTP fixtures verify wire fields, numeric codes, type-specific classification, stage, safe fields, seconds/date retry guidance. |
| `pkg/connector/loginerrors.go`, `connector.go`: surfaced safe errors | [login_error_logging_test.go](../pkg/connector/login_error_logging_test.go): `TestSubmitUserInputLogsStructuredLoginErrorWithoutCredentials`, `TestSessionEstablishmentErrorKeepsStageAndSafeMessage`; [auth_error_test.go](../pkg/line/auth_error_test.go): `TestSanitizeProviderMessage`; [qr_failure_test.go](../pkg/connector/qr_failure_test.go): `TestQRVerificationTemporarilyUnavailable` | Capture structured logs/errors; no fixture secrets; safe provider text retained, unsafe substrings suppressed/redacted. |
| `pkg/connector/login_cooldown.go`, `client.go`: retry safety | [login_cooldown_test.go](../pkg/connector/login_cooldown_test.go): `TestQRRateLimitIsReportedAndBlocksImmediateRetry`, `TestLoginClaimSerializesSameAccount`, `TestLoginCooldownUsesRetryAfter`, `TestStoredCredentialRecoveryRespectsActiveCooldown`, `TestStoredCredentialRecoveryStartsCooldownAfterProviderLimit`, `TestStoredCredentialRecoveryHonorsHTTPRetryAfter` | Count fake requests, no extra provider call during wait; timers/claims scoped by Matrix user; stored credentials cannot bypass active cooldown. |
| `pkg/connector/resolve_identifier.go`, `userinfo.go`: lookup/portal | [resolve_identifier_test.go](../pkg/connector/resolve_identifier_test.go): `TestResolveIdentifierPublicMIDAndSearch`, `TestResolveIdentifierFailuresDoNotPersistPortals`, `TestResolveIdentifierConcurrentCanonicalPortalReuse`, `TestStartChatCreatesReusesAndRetriesRoom` | Temp DB + framework provisioning path; validated public-ID/MID key, no failed-lookup persistence; create, reuse, creation retry and reopen after leaving. |
| Same plus `pkg/line/user_mid.go`: strict identity | [opaque_mid_test.go](../pkg/connector/opaque_mid_test.go), [user_mid_test.go](../pkg/line/user_mid_test.go), [identity_audit_test.go](../pkg/connector/identity_audit_test.go): `TestLookupToOutboundIdentityRepresentations`; resolver `TestResolveIdentifierDeadlineAndMismatchedMID`, `TestSearchUsersRejectsUnrequestedContactMID`, `TestSearchUsersEmptyQueryDoesNotEnumerateContacts` | Case preserved for gateway IDs, canonical legacy IDs, reject mismatched/unrequested/ambiguous result, no empty-query enumeration, same outbound recipient. |
| `pkg/connector/send_message.go`, `pkg/line/methods.go`: send/ack | [outbound_pipeline_test.go](../pkg/connector/outbound_pipeline_test.go): `TestOutboundOwnerAndCanonicalRecipient`, `TestOutboundRequestCancellation`; [send_message_response_test.go](../pkg/line/send_message_response_test.go): `TestSendMessageRejectsMissingAcknowledgement`, `TestSendMessagePreservesAuthenticationError` | Correct sender/recipient/login, one mocked normal send, no false success on reject/empty ack; blocked HTTP observes context cancellation. Not exactly-once under lost acknowledgements. |
| `pkg/connector/media.go`, handlers: integrity | [media_hmac_test.go](../pkg/connector/media_hmac_test.go): `TestMediaHMAC`; [handler_test.go](../pkg/connector/handlers/handler_test.go) | Real local encryption/MAC fixtures for image/file/audio/video/thumbnail; tampered ciphertext/MAC and wrong MAC mode rejected; handler key/decrypt routing. |
| `pkg/connector/handlers/image_media.go`, `image.go`, `text.go`: metadata/previews | [image_media_test.go](../pkg/connector/handlers/image_media_test.go): `TestLineImageMediaInfo`, `TestLineImageBrands`, `TestLineImageExtensions`; [text_test.go](../pkg/connector/handlers/text_test.go): `TestConvertTextUploadsOnlyImageLinkPreviews` | Generated image/config fixtures and bounded brand parsing; mock download/upload verifies preview MXC, no raw CDN image reference; failures retain text. |
| Test/runtime support, CI/build scripts | [runner_secret_test.go](../pkg/runner_secret_test.go), [memory_test.go](../pkg/ltsm/memory_test.go), [coverage checker tests](../scripts/test_check_test_coverage.py) | Native heap abort/recovery remains exercised; deterministic concurrency fixtures; checker rejects coverage regression. Repro build is historical measured evidence above, not a claim about cross-host builds. |

## Safe live replay order

Use a **disposable/secondary LINE account** and a consenting test contact. Record
the exact binary version/hash and source; use a separate bridge/test data set.
Back up binary, config and database using your normal consistent backup process.
Do not attach a second Chrome-extension client: it can invalidate the bridge's
session. The branch currently conflicts with main; test the isolated branch,
not an unreviewed conflict resolution. Do not overwrite an important deployment
just to replay this matrix.

Commands below are in the bot management room. In other rooms prefix with your
configured prefix (for example `!line`). `<LINE_ID>` means an exact public ID
shared by the test contact, **not** their display name. `<MID>` is a verified
internal user identifier, not a guessed `M_id` string. With several logins,
select the intended login using framework help before any create/send action.

| ID / feature | Preconditions | Exact command or action | Expected result / prior behavior | Status | Risk | Cleanup / recovery |
| --- | --- | --- | --- | --- | --- | --- |
| S01 startup/session | Isolated deployment with preserved existing session | Start test bridge normally; inspect health and management-room `list-logins` | DB/appservice initialize; existing login stays usable where provider permits. Not a new login request | LIVE VERIFIED for historical `f36c646b` startup only; MANUAL NOT RUN on current PR integration | Medium | Stop test bridge and restore backup on fatal startup; never delete sessions to fix a symptom |
| L01 help/unknown | No login required; command permission | `login`, then `login help`, then `login invalid` | Friendly options/guidance, no provider auth; before: full IDs / invalid help | AUTOMATED PASS; MANUAL NOT RUN with exact-head live evidence | Low | None |
| L02 disabled QR | Separate test config with existing `network.qr_login: false` | `login help`, then `login qr` | No QR advertised; useful unavailable error and no QR request | AUTOMATED PASS; MANUAL NOT RUN | Low/medium (config restart) | Restore original option; restart only test bridge if changed |
| L03 email/full ID selection | Only if testing a new disposable session is necessary | `login email`, then `cancel`; alternatively `login dev.highest.matrix.line.email_login` then `cancel` | Credential prompt, no password in command history; full IDs remain valid | AUTOMATED PASS; MANUAL NOT RUN | Medium | Cancel; do not submit primary credentials or duplicate login attempts |
| Q01 one QR lifecycle | QR enabled; login actually needed, mobile available | **Choose one**: `login qr` or `login dev.highest.matrix.line.qr_login`; scan once; approve/PIN only in expected UI | QR → approval/PIN if requested → validated session; before had concurrency/deadline error problems, not missing QR protocol | AUTOMATED PASS; MANUAL NOT RUN per-scenario live evidence | High | `cancel` if abandoned; stop on rejection and observe provider guidance; do not loop |
| Q02 overlap/cancel/expiry | Prefer automated test; optional disposable pending QR | Abandon one pending attempt with `cancel`; for expiry let it expire naturally | No persisted incomplete login; claim released; errors distinguish deadline/cancel | AUTOMATED PASS; MANUAL NOT RUN | High | Cancel only pending login; do not repeatedly generate QR codes for stress testing |
| A01 restriction/provider messages | Only a naturally occurring 429, RETRY_LATER, or verification-unavailable error | Record displayed category/stage and UTC, not QR/token/body; do not induce rate limit | Useful safe message + wait; defaults 15/5/10 min, Retry-After can extend. Duration is local policy, not LINE restriction duration | AUTOMATED PASS; NOT TESTABLE WITHOUT PROVIDER CONDITIONS | High | Honor wait; no bypass/restart to clear local gate; use mocked tests for immediate-retry assertions |
| I01 resolve public ID | Logged-in secondary account; known consenting contact | `search <LINE_ID>`; optionally `resolve-identifier <LINE_ID>` as listed in framework help | Validated exact contact; lookup-only does not create a DM; no false “shadow ban” inference from failure | AUTOMATED PASS; MANUAL NOT RUN | Medium | Stop on mismatch; do not contact unverified results |
| I02 create/reuse/reopen | I01 succeeds, intended account confirmed | `start-chat <LINE_ID>`; repeat once; optionally `start-chat <MID>` for same contact; leave test room then `start-chat <LINE_ID>` | Same canonical recipient/login; reuse/rejoin existing room where possible, not duplicate portal; before public string could create wrong portal key | AUTOMATED PASS; MANUAL NOT RUN | High (rooms/membership persist) | Stop if wrong recipient/duplicate; do not delete DB rows manually; keep IDs private for operator diagnosis |
| I03 malformed/ambiguous IDs | Automated only for synthetic server results | Run resolver/MID tests above; do not deliberately query strangers | Reject mismatched/ambiguous/unrequested MID before portal persistence; opaque case unchanged | AUTOMATED PASS; NOT TESTABLE WITHOUT PROVIDER CONDITIONS for malformed server data | High | No live fault injection needed |
| M01 outbound | Known correct test portal; cooperating contact | Send one neutral unique text from Matrix, inspect LINE receipt | Server ack maps to event; one observed message; before empty ack could look successful | AUTOMATED PASS; MANUAL NOT RUN | High | Do not resend on timeout until recipient checks; deletion not required |
| M02 inbound/attribution | M01 verified | Contact sends one neutral text from LINE to Matrix | Correct room/sender, one observed event; inbound text routing largely pre-existing | MANUAL NOT RUN; automated identity tests cover only part of this | Medium | Stop on unexpected portal/sender; preserve sanitized timestamps only |
| M03 E2EE warning | Existing encrypted test room | Observe inbound puppet event for “sender ... does not match ... owner of the device” | Known unresolved warning; no fix claimed | KNOWN LIMITATION | High if mistaken for delivery/integrity guarantee | Do not reset crypto/devices or disable encryption to hide warning |
| F01 media | Correct test portal; non-private fixtures | Contact sends one small JPEG, PNG, supported video, audio and file; inspect dimensions/MIME/download in Matrix | Valid media displays; MIME/name/size and dimensions where supported; authenticated bytes only | AUTOMATED PASS for synthetic integrity/metadata; MANUAL NOT RUN across live formats | High | Stop on HMAC failure; do not disable verification; keep original fixture privately |
| F02 corruption / unusual formats | Local fixtures only | Run `TestMediaHMAC` and `TestLineImageBrands`; optional benign WebP/HEIC/AVIF from disposable contact | Tampered fixture rejected; unsupported dimensions may be absent; not universal codec support | AUTOMATED PASS; MANUAL NOT RUN for live format combinations | Medium/high | No tampering with production traffic; no repeated OBS retry workaround |
| F03 link preview | Benign non-private URL, previews enabled | Send one text with a harmless public link in test chat | Image preview uploads to Matrix; failure still delivers text (upload behavior already in base) | AUTOMATED PASS; MANUAL NOT RUN | Medium (downloads) | Disable only test preview if needed; don't publish URLs/raw errors |
| R01 send cancellation | Automated mock preferred | Run `TestOutboundRequestCancellation`; do not disconnect a production bridge mid-send | Mock request canceled promptly; live uncertain delivery remains possible | AUTOMATED PASS; MANUAL NOT RUN for real interruptions | High | No blind resends; verify recipient before retry |
| R02 reconnect/session | Only on isolated bridge with known session | Optional orderly test-bridge restart once; verify `list-logins` and one controlled message | Stored state retained where possible; passwordless failed recovery asks fresh approval rather than inventing credentials | AUTOMATED PASS for selected recovery cases; MANUAL NOT RUN across network/session matrix | High | Restore backup if necessary; do not invalidate mobile account or trigger repeated auth |
| O01 sync/log review | UTC window around above tests | Inspect structured fields locally, see safe projection below | No new bulk-sync policy; existing connect/fullSync sync still runs; exact lookup avoids contact enumeration | MANUAL NOT RUN for exact-head live network frequency; code-diff evidence | Medium/privacy | Stop on unintended portals or request bursts; don't publish raw logs |

### Safe diagnostic collection

Prefer existing structured logging; no new config key or unrestricted HTTP-body
logging is needed. Logs can contain MIDs, Matrix IDs, message IDs, URLs and error
text even when the new auth messages are sanitized. Inspect a narrow UTC test
window locally. For JSON logs, a public-shareable **numeric/boolean-only**
projection can be made without dumping raw production data:

```sh
# Set an ISO UTC START/END and your actual log path locally.
jq -c --arg start "$START" --arg end "$END" '
  select(.time >= $start and .time <= $end) |
  {time, http_status, line_response_code, line_error_code,
   retry_recommended, temporary, cooldown_triggered, success} |
  with_entries(select(.key == "time" or
    (.value | type) == "number" or (.value | type) == "boolean"))
' /path/to/logs/bridge.log
```

Verify your logger's time field/format first (it may use `timestamp` instead of
`time`). Privately correlate stage/category and identifiers if necessary, then
manually redact before sharing. Never publish QR/session tokens, credentials,
cookies, encryption keys, private text, complete logs, or raw provider responses.

## What has NOT been established

No complete live multi-account/login/client matrix; no proof of every LINE
restriction category or its duration; no guarantee against account restrictions;
no exhaustive network-interruption/reconnect testing; no exactly-once delivery
under lost acknowledgements; no all-format/all-size media, group/LS-on/LS-off,
direct-media or all-Matrix-client coverage. No new-current-upstream merged-tree
validation, GitHub CI success, E2EE device-ownership warning fix, or historical
per-scenario live inventory. Optional lookup-shape diagnostics remain deferred.
