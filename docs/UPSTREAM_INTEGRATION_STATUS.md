# Upstream integration status

## Verified sources

- Current upstream baseline fetched from `origin/main`: `45a48b9b3b57330d99d326d443f9a9fc8725a48f`.
- Immutable working checkpoint: `f36c646b468719877c9fb4329f2cab18b652fbca`, branch
  `checkpoint/line-feature-complete-20261029`, tag
  `checkpoint/line-feature-complete-20261029-f36c646b`.
- Upstream integration branch `upstream/line-stabilization-20261029` merges the
  checkpoint with the current upstream baseline. It retains upstream #296's
  bounded/cancellable search and avatar request improvements alongside the
  checkpoint's exact ID validation, contact-result checks, and portal reuse.
- Working release v1.2.0+dev.f36c646b is still the byte-identical binary running
  on `element-chat`; no service was changed in this integration task.

## Historical PR relationships

- `beeper/line#271` is still open (head `meramsey/line:integration/linux-build-20261020`,
  SHA `587164f7ced99bc5765ac7df4eafc11314e6d602`). Its eight patches are present
  in the checkpoint by patch equivalence. This integration is intended to
  supersede #271 because it contains those features plus follow-up authentication,
  identity, contact validation, media, CI, and release corrections. #271 remains
  open and untouched; do not merge both versions without comparing their diffs.
- `meramsey/line#1` remains open against the fork's integration branch (head
  `a7b5764a6a4c0d45d10814fd101c21eca70199b9`). Relevant bounded test modes,
  deterministic concurrency fixtures, native heap test support, coverage gates,
  and CI caching are adapted in this integration. Historical benchmark numbers
  in its old performance report are intentionally not copied as they are
  environment-specific.
- Upstream commits already incorporated include latest #296 (`45a48b9`), which
  bounds search results and honors cancellation. The integration retains this
  behavior rather than reverting it.

## Feature map

| Area | Implementation | Regression coverage | Status |
| --- | --- | --- | --- |
| Friendly login aliases and QR selection | `pkg/connector/login_command.go`, `connector.go` | `login_command_test.go` through framework Processor | In new PR |
| QR lifecycle, auth stages, sanitized provider errors, 429/Retry-After and per-login cooldown | `login_qr.go`, `loginerrors.go`, `login_cooldown.go`, `pkg/line/{qr,auth_error,errors}.go` | QR, error, and cooldown suites | In new PR |
| Public ID, canonical/opaque MIDs, validation, portal reuse/reopen | `resolve_identifier.go`, `userinfo.go`, `pkg/line/user_mid.go` | resolver, opaque MID, and portal tests | In new PR; preserves upstream #296 limits/cancellation |
| Send cancellation/acknowledgements, inbound media auth and metadata | `send_message.go`, `media.go`, handlers | outbound, media, image metadata tests | In new PR |
| CI caches, test modes, coverage gate, reproducible build | `.github/workflows/deploy.yml`, `scripts/`, `build.sh` | release validation | In new PR |
| Optional lookup-shape diagnostics | No current implementation | No current test | Intentionally deferred pending a privacy/schema review; current safe validation and errors remain |

## Validation

On the integration branch (source merge `07badf92460e5ae744b90d1c0c0a46cc30396218`,
plus this status-documentation commit):

- Normal: 512 passing test/subtest events, one skip, zero failures.
- Race: 512 passing test/subtest events, one skip, zero failures.
- Coverage: 512 passing test/subtest events, one skip, zero failures; all package floors passed.
- Release: all checks passed, including module verification, static analysis, build, and the three suites above.
- Targeted login, search/identity, portal, QR, auth error, and cooldown tests passed.

## Publication state and remaining work

- Checkpoint branch/tag are published to the `meramsey/line` fork.
- The upstream integration branch is prepared for a replacement PR targeting
  `beeper/line:main`. GitHub CLI has no authenticated session in this environment,
  so PR creation requires interactive authentication. The pushed branch's
  compare URL is `https://github.com/beeper/line/compare/main...meramsey:line:upstream/line-stabilization-20261029`.
- No feature claim should imply upstream coverage until the replacement PR is
  reviewed/merged. Do not close PR #271 or modify PR #1 automatically.
- Live QR/provider behavior and inbound puppet E2EE sender/device warnings remain
  operator-verification items. Matrix encryption was not altered.
