# Direct chats, synchronization, outbound messages and QR login

## Starting a direct chat

In the bot management room:

```text
search carol.id
start-chat carol.id
start-chat u0123456789abcdef0123456789abcdef
```

These are illustrative identifiers, not live test accounts. In another Matrix
room, use the configured command prefix (default `!line`), for example
`!line start-chat carol.id`. With several LINE logins, the framework also accepts
`start-chat <login MID> <recipient LINE ID or MID>`.

Public LINE IDs use the existing `TalkService.findContactByUserid` RPC. The
bridge validates the returned user MID before creating a ghost or portal. User
MIDs are `u` followed by 32 hexadecimal digits; surrounding whitespace and input
case are normalized. A direct MID uses cached contact data or `getContactsV2`,
with the existing BuddyService fallback for official accounts. The bridge never
chooses a recipient by display name. `search` can still return display-name
matches when there is no exact ID match, but `start-chat` cannot use that fallback.

Both commands share exact identifier resolution. A public-ID result already
contains the contact profile: it is reused for ghost/DM information rather than
re-fetching the same contact. A public ID is resolved afresh on each request so a
reassigned public ID cannot use a stale ID-to-MID alias cache.

The former implementation treated every argument as a MID, created a portal
record immediately, and ignored the error returned by `GetChatInfo`. An argument
beginning with `c` was then routed to `getChats`; an empty result produced
`chat not found`. Framework room creation retried this missing chat-info lookup
and reported `Failed to create portal room`.

The corrected response contains the canonical MID, login-scoped portal key and
complete DM information. bridgev2 retains responsibility for its locked portal
cache, database identity, room-creation event loop, membership checks and reuse.
Lookup-only requests do not create portal records. Failed remote lookups create
neither invalid portal records nor public-ID aliases. If Matrix room creation
fails later, bridgev2 retains one valid canonical row with no room ID for retry;
deleting it blindly would race another request. Leaving an existing room follows
bridgev2's existing orphaned-DM cleanup/recreation behavior.

Historical invalid public-ID portal records are not migrated or deleted
automatically. Review such records separately before any production cleanup.

## Why many conversations may appear

Neither exact ID resolution nor its error path calls conversation synchronization.
The search-only display-name fallback enumerates contacts, not conversations, and
does not create portal rooms. Authentication recovery on these calls refreshes or
re-authenticates the session without invoking `Connect` or a broad chat sync.

Existing legitimate broad synchronization sources are:

* `LineClient.Connect`: group synchronization, DM synchronization and recent-message
  prefetch on connection/startup.
* LINE SSE `fullSync`: refreshes block state, group/DM state and recent messages;
  this can occur when server/client revision state requires reconciliation.
* Recent-message backfill containing system events: re-fetches current group state
  after replay so historical membership/name events cannot leave stale state.

New `sync_trigger` log fields identify `connect`, `line_full_sync`, and
`backfill_state_restore`. Existing portal mappings, message deduplication rows,
LINE revision state and persisted chat-deletion cutoffs affect what those paths
create or skip. No legitimate sync path has been disabled. The reported temporal
association with `start-chat` is not sufficient to establish a causal connection.
The earlier #196/#189 integrations changed media processing, not these sync paths.

## Diagnosing an outbound message

Trace one Matrix event ID rather than retrying across unrelated rooms/accounts:

1. Matrix connector filtering and decryption. Relevant messages include
   `Dropping ... event from user with no permission to send events`,
   `Dropping unencrypted event as encryption is configured to be required`,
   `Decrypting received event`, and `Failed to decrypt event`.
2. Portal lookup and `FindPreferredLogin`. A login-scoped portal uses its receiver
   login. A different Matrix sender cannot use that login unless an explicitly
   configured relay permits it. `Failed to get user login to handle Matrix event`
   distinguishes this stage. Working bot commands do not establish that every
   portal event has the same sender/login association or encryption session.
3. `Handling outbound Matrix message for LINE`: the connector has been reached.
   Correlation fields include `matrix_event_id`, `matrix_sender`, `line_login_id`
   and `line_recipient`. The recipient is the portal's canonical remote ID.
4. Message conversion and Letter Sealing selection/encryption, then the signed
   `TalkService.sendMessage` RPC. `Finished LINE API send attempt` records
   `success` and, on acknowledgement, `line_message_id`. Message bodies, access
   tokens and QR approval data are not included in these new diagnostics.
5. The returned LINE message ID becomes the bridge message record. bridgev2 sends
   the corresponding success/error status. An API acknowledgement is not proof of
   recipient-side delivery/read status.

Three defects were reproduced with mock responses and fixed: the send RPC did not
inherit the Matrix request context; HTTP-200 error wrappers lost TalkException
codes required by existing recovery logic; and a missing acknowledgement could
produce success with an empty ID or a nil dereference. Authentication/permission
rules, supported encryption paths and inbound conversion are unchanged.

The actual Element delivery incident still needs a failing event's correlated
stage/error/status to identify its cause. Useful evidence is the event ID, sender,
portal receiver/login ownership, and the messages above, with tokens, message
bodies and approval codes removed. Do not infer a Synapse connectivity failure
from this report; command delivery and MID-based portal creation already work.

## QR login: separate state machine

Enable `network.qr_login: true` in the runtime configuration, then use:

```text
login dev.highest.matrix.line.qr_login
relogin <login MID> dev.highest.matrix.line.qr_login
cancel
```

Outside the management room, prefix these with `!line` (or the configured prefix).
The bridge requests a session and QR callback, adds the login public key, and
returns a display-and-wait step. bridgev2 renders the QR image for Matrix. After
LINE scan approval, a reusable certificate may satisfy verification; otherwise
the user must enter/approve a PIN on the primary device. Only then does token
exchange occur. The bridge verifies the token's profile/MID and required E2EE
keys before persisting a login, and checks cancellation/account identity during
finalization.

Overlapping Start/Wait operations are now rejected instead of consuming another
waiter's polling result or blocking indefinitely. Startup deadline errors retain
`context.DeadlineExceeded`, rather than always becoming `context.Canceled`.
Cancel still interrupts the underlying attempt without waiting on the operation
guard. Expired polling is retried only within the existing server count/attempt
deadline; exhausted verification requires a fresh login attempt and QR scan.
There is no new speculative refresh endpoint or device-approval bypass.

V3 refresh tokens are persisted by the existing finalizer. If refresh fails for a
passwordless QR account, reconnect must request a new QR approval rather than
inventing email/password credentials. Mock tests cover this and ensure that a
token response without a usable required keychain does not replace the stored
session. These lifecycle fixes do not establish the cause of an unspecified live
QR scanning/token-exchange failure. Successful live QR login and reconnection
have not been validated in this workspace.

## Test boundaries

New tests use mock LINE HTTP responses and isolated SQLite databases. They cover
shared resolution, invalid identities, lookup failures, cancellation/deadlines,
canonical portal-cache concurrency, existing-room reuse, creation failure/retry,
reopening after leaving, outbound ownership/routing/error propagation and QR state
transitions. The production PostgreSQL database and live Matrix/LINE accounts are
not used. Full live command concurrency, recipient delivery, and successful live
QR/session restoration require operator validation after deployment; mocked tests
are not a claim that those account-specific workflows have been verified.
