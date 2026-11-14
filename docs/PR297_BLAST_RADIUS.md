# PR #297: blast radius and review boundaries

This is a broad runtime + test/CI PR, not a low-risk documentation-only feature.
The **current update** adds documentation only. Refer to the
[behavior comparison](PR297_BEFORE_AFTER.md) and [test evidence/replay](PR297_TEST_MATRIX.md).

## Impact by subsystem

| Subsystem / class | Runtime and users affected | Configuration / persistence | Requests / error policy | Potential failures and validation priority |
| --- | --- | --- | --- | --- |
| Login command UX (user-visible) | Matrix users with login permission invoking `login`; provisioning still uses existing flow IDs | Existing `network.qr_login`; no new required option. Framework permissions remain | Help/invalid/disabled methods do not start provider auth; aliases delegate to same flow | Handler registration/permissions regression; test actual Processor dispatch, disabled QR and full IDs before live auth |
| QR/email auth and stored-credential recovery (high, account safety) | New QR/email sign-ins, re-authentication, token fallback, session establishment | Claims/cooldowns are memory-only, keyed by `User.MXID`; existing stored credentials, certificate, token/key metadata and finalization reused | Overlap blocked, wait enforced before new auth request including stored credentials. Default rate/temporary waits plus longer Retry-After. Not an automatic retry scheduler | Can delay legitimate reconnect or suppress useful provider wording; different Matrix users for same LINE account not coordinated; one Matrix user's several logins share gate. Test cancel/error claim release and stages; use disposable account. Restart clears local cooldown, not LINE's restriction; never recommend restart as bypass |
| Identity lookup, search, portals (high, routing) | Public ID start-chat, lookup-only, MID input, contact search/list | Validated canonical MID + receiver forms portal key; existing portal DB APIs reused; no rewrite of old bad public-ID portals | Public IDs resolved afresh; contact cache keyed by MID. Exact result returns without contact enumeration; fallback search still batches contacts, max 20 results from #296. Empty search no lookup. Errors now visible rather than empty successful result | Unsupported real IDs/provider shapes rejected; old malformed portals can coexist until operator-reviewed cleanup; wrong-recipient routing is critical. Verify known contact, mismatched IDs and portal reuse/reopen before sending |
| Outbound send/error/ack path (high, reliability) | Matrix-to-LINE text/media send paths using sendMessage | Existing sequence allocation/retry/DB ack mechanisms retained; no new persistent dedup store | Pass caller context; preserve provider errors for existing recovery; reject empty ack ID | Cancellation/timeout may occur after LINE accepted message. Re-sending can duplicate; one mock request is not a global exactly-once guarantee. Test failures/ack absence and verify with recipient before live resend |
| Inbound media / metadata (high, integrity/compatibility) | Encrypted image/file/audio/video/thumbnail paths, including shared handler/direct-media decryption; inbound image metadata | No new option; no key/device reset or crypto-store change | Authenticate before plaintext; video-specific MAC input. No new 60-retry OBS policy. MIME/size/dimensions based on received image bytes | Previously tolerated unauthenticated bytes now rejected; real unsupported MAC formats could fail. Synthetic crypto is not full server compatibility. Test each supported type with benign small fixtures. MIME recognition doesn't imply HEIC/AVIF decoder/transcoder coverage |
| Preview/log/sync changes (internal reliability/privacy) | Preview failures, login/send diagnosis, conversation sync logs | Existing structured logging; no new debug config or raw HTTP logging | Preview upload already upstream; safer failure logs and test seam here. Sync policy unchanged: connect/fullSync/backfill reconciliation remains, only trigger labels added | Existing logs still carry sensitive IDs/URLs/error context; sanitize. No claim of eliminating bulk sync or all raw error exposure |
| Native test support / fixtures (mostly test, small runtime API surface) | `pkg/ltsm/memory.go` exposes existing malloc/free wrappers for bounded heap fixtures; other changes in tests | No generated WASM crypto edits, no default runtime policy change | No LINE traffic in test suite | Memory wrappers must only be used correctly; tests retain real exhaustion/recovery, isolation and stress loops. Not a speculative protocol change |
| Test/CI/build infrastructure (not bridge request behavior) | Developers, fork/upstream Actions, build consumers | Per-package floor file, Python checker, scripts; no production config required | Local fast/normal/race/coverage/release; CI normal/race/coverage and caches; fork Docker pushes and deployment restricted | CI resource/cache/coverage regressions; source checking is not live compatibility. Build uses fixed epoch optionally and explicit tag/SHA; same source with different tag state can hash differently |

## Database and stored-state review

The diff against the integrated upstream base changes **no SQL/migration/schema
files**, no `go.mod`/`go.sum` framework version, and no fields in
`UserLoginMetadata` or its JSON tags. There is no PR-specific schema migration.
Normal framework DB version checks still run at startup; deployments from other
versions must follow their framework's migration rules.

Persistent effects still exist: successful login finalization stores tokens,
certificates and E2EE metadata using existing mechanisms; successful canonical
resolution/creation writes ghosts, portals and Matrix room/membership state.
Failed lookups now avoid creating portal rows. No backfill/rekey/migration of
previous wrongly keyed portals is included. Cooldown/active-claim maps are not
persisted. Existing Matrix crypto state/encryption devices are not reset,
disabled or repaired by this PR. The known puppet sender/device ownership
warning remains unresolved.

## What is untouched / inherited, not a new fix

- Homeserver configuration, Synapse, appservice registration format and database
  schema are not changed by the PR.
- Existing upstream media-size handling at the integrated base, HEIC/audio/
  sticker improvements, bridgev2 room creation and send retry/sequence mechanisms
  are not replaced wholesale. Later main changes need separate reconciliation.
- Group-message key-spec logic has additional tests here, not a new algorithm.
- No new auto-login probing, restriction bypass, contact mutation policy,
  unconditional room enumeration policy or Matrix encryption weakening.

## Upgrade / rollback guidance for a reviewer

Prefer an isolated bridge with a disposable LINE account. Save exact binary,
config and consistent DB backups before installing; keep a test window and
record source/SHA. Run startup + help/lookup before login/send/media tests.
Avoid running both bridge and Chrome-extension session on the same account.

Binary rollback is straightforward mechanically (restore preserved binary with
original owner/mode, restart **only the test bridge**) because no PR-specific
schema migration is introduced. It is not a full state rollback: server-accepted
messages, remote session invalidation, newly created portals, room membership and
new login metadata cannot be undone by replacing the executable. Preserve state
for diagnosis; do not delete credentials/DB rows, restore a running DB blindly,
retry an ambiguous send, reset crypto devices, or disable encryption as recovery.
If unexpected identity/portal routing or request bursts appear, stop the test
and report a sanitized timestamp/category rather than probing more accounts.

## Separate CI dependency issue found during documentation review

The normal test job's explicit alias step runs `scripts/test.sh fast`, which
invokes `staticcheck`. That job installs Go and libolm but does not install
`staticcheck`; the separate lint job's installation does not share its runner.
On a runner without that tool the alias step will fail before tests. Local
validation had the tool on PATH; GitHub reports no checks here, so this is a
workflow dependency gap found by source inspection, not an observed CI failure.
It needs a focused follow-up (install the tool in that job or use an appropriate
test-only invocation while retaining lint elsewhere). No workflow/application
change was folded into this documentation commit.

## Suggested smaller review units

The reviewer explicitly asked for smaller chunks. A sensible follow-up split is:

1. **Identity/DM resolution:** resolver + context-aware lookup RPCs + MID and
   portal tests; preserve #296 search cap and newer avatar changes.
2. **Authentication/account safety:** QR lifecycle + typed/sanitized errors +
   HTTP Retry-After + shared claims/cooldowns + reconnect tests. These are
   coupled and should be validated together with upstream #294/#298.
3. **Messaging/media:** context-aware sends and ack validation, and separately
   authenticated inbound media/image metadata with #189/#196 attribution.
   Identity-aware send tests currently reuse resolver fixtures; extract shared
   fixture support or state an explicit test dependency rather than duplicate it.
4. **Test performance/CI/build:** deterministic fixture and native heap support,
   then scripts/coverage/cache/fork safeguards and reproducible build metadata;
   coverage baselines must be measured for each resulting runtime branch.

These are proposed boundaries, **not already-created independent PRs**. #297
consolidates overlapping #271 and fork #1 work; those PRs remain open and their
branches untouched. Do not merge overlapping versions independently. Current
main `10e0a5f` conflicts in `client.go`; resolve/review on separate focused
branches with fresh tests before treating any split as merge-ready. No runtime
conflict resolution or history rewrite was done for this documentation request.
