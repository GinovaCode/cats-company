# SkillHub publication entry persistence

This implements staged CatsCo foundations of the
[approved three-repository contract](https://github.com/buildsense-ai/catsco-skillhub-cloud/blob/c369cc9d5e956a4c42095ebcbb93b4ac7506e5f0/docs/api/publication-entrypoint-delegation.md).
It adds a PostgreSQL store for durable **workspace publication intents**, a
default-off private status API and a default-off, source-verified workspace
entry protocol, plus opt-in durable background source probing. It does not upload
or publish files or issue delegated credentials.

## Implemented behavior

The optional `store.SkillHubPublicationEntryStore` creates an immutable entry and
a reference-only `capture_candidate` outbox row in one transaction. The optional
worker below can lease it for read-only source probing, not capture. Creation
always returns `waiting_runtime`, never a claim
that a file was captured, validated, uploaded or published.

The database checks an active human account, an active enabled Bot owned by that
human, and the Bot's durable body binding. These rows stay read-locked through
insertion, serializing account disablement, owner transfer and body changes
against admission. This is necessary but **not sufficient** runtime authorization:
the workspace protocol authenticates the human and verifies device,
installation and route generation against server facts, and asks the exact
runtime to confirm the workspace generation/local Skill reference. Future
capture/upload consumers must recheck those facts. Caller-provided UIDs or copied
record fields are not authority. Do not blindly decode HTTP bodies into this store.

Exact `(actor UID, request ID, immutable input)` replay returns the same entry
without another event or quota charge. A conflicting input is rejected. The
server generates the entry ID and timestamp. Identity hashes bind every input
field under `catsco-publication-entry-v1`; they are **not** file/candidate hashes.
Same-name Skills and Bots never participate in identity lookup.

Reads are limited to the original active human publisher. A new Bot owner or
friend cannot read the previous publisher's entry; the original publisher can
read historical intent after transfer, but cannot replay it as a new authorized
operation. The read API below authenticates the human and explicitly projects
safe fields. The store's internal records must not be serialized wholesale into
chat, browser responses or logs.

## Storage and limits

The additive tables are registered in `server/db/postgres/schema.go` and mirrored
in `000028_skillhub_publication_entries.up.sql`, following the dual-track policy
in [DB_MIGRATIONS.md](DB_MIGRATIONS.md). `CreateSchema` adds them idempotently on
the next normal startup; external migration tools can apply the same DDL. A test
checks that both definitions match. Store operations execute no runtime DDL.
Startup schema failures retain existing startup behavior;
missing tables in an already-running process return an error, never memory-only
success. MySQL does not implement this optional store; later routes must report
unsupported storage rather than silently switch adapters or use an in-memory job.

| Table | Contents |
| --- | --- |
| `skillhub_publication_entries` | Immutable publisher/Bot/source binding, request and identity hash, initial state and server timestamp. |
| `skillhub_publication_outbox` | One reference-only capture event per entry; no payload or credentials. |
| `skillhub_publication_admission` | One row serializing quota checks and insertion across API processes. |

Default admission budgets are 10,000 total entries, 100 per publisher, and 20 new
entries per publisher per minute. Caller policies are internal and validated;
different processes must use the same policy. Replays are checked after current
authority but before rate/storage admission. Global locking keeps concurrent
new requests from exceeding a shared configured budget. Each creation has a
10-second context deadline, 3-second lock timeout and 5-second statement timeout;
reads have a 5-second context deadline. There is no network or file work inside
the transaction. Database errors must be sanitized by future handlers.

No cleanup or expiry is implemented in this slice. At capacity, new entries fail
closed; no active intent or replay evidence is evicted. Future retention work
must preserve accepted-commit evidence and must not reuse expired request keys
automatically. Actor/Bot IDs are immutable historical values without foreign keys
to `users`, so these inert records do not block existing account deletion. Creation
checks the real rows transactionally; subsequent operations must recheck authority.
Outbox rows reference the entry with a restrictive foreign key.

## Private status API

`CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED` defaults off; only `1` or `true`
(case-insensitive, trimmed) enables reads. Workspace creation requires the separate
flag and protocol below; there is no HTTP creation route.
The handler authenticates an active CatsCo human JWT from one `Authorization:
Bearer ...` header itself, including a fresh account-state check. It accepts no
query credentials, API keys, cookie-only sessions, caller UID, query parameters,
request body or arbitrary source path. A display name is never an identity key.

| Route | Behavior |
| --- | --- |
| `GET /api/skillhub/publications/capabilities` | Available to authenticated humans even with reads off. Returns `schemaVersion: 1`, effective `entryReadsEnabled`, effective `sourceStatusReadsEnabled`, effective `workspaceCreateEnabled`, `chatCreateEnabled: false`, and `minPollIntervalMs: 5000`. Adapter support is not a database or target-runtime health check. |
| `GET /api/skillhub/publications/entries/{entryRequestId}` | When enabled on a supported store, reads the original publisher's record. Missing, wrong-publisher and disabled reads return the same 404. Bot ownership transfer does not grant historical record access to the new owner. |
| `GET /api/skillhub/publications/requests/{requestId}` | Same private projection, using the original account-scoped request ID to recover a lost creation response. This does not reauthorize, create, replay or mutate an entry. |
| `GET /api/skillhub/publications/entries/{entryRequestId}/source-status` | Original publisher's historical source-probe progress, using the same read flag and private transport guards. Requires the optional PostgreSQL source-status reader. Never claims, settles or resumes a task. |

The entry response contains only `schemaVersion` and `entry` with
`entryRequestId`, decimal-string `botUid`, `entrypoint`, `state` and UTC
`createdAt`. It omits request keys, publisher identity, device/body/installation,
workspace/source references, identity hash, candidates and credentials. The only
currently supported state is `waiting_runtime`; it means durable intent, not
capture, validation or publication success. Unknown states fail closed with 503
until their safe projection is implemented. No Cloud report is fetched here.

Every handler response, including authentication failures, is `private, no-store`
JSON. Invalid credentials return 401; disabled/non-human accounts 403; malformed
read transport 400; non-GET methods on known routes 405; throttling 429; unsupported
storage, unavailable authority/storage or an unknown record format 503. Unknown
routes/IDs return 404. Errors use stable `publication.*` codes, a generic message
and (for 503) a server-generated correlation ID logged without raw errors, tokens
or source data. They are not validation rejects and must not trigger model repair.

Polling is bounded per API process: 60 requests/minute per authenticated account
with burst 10 (including capabilities), and 12 entry reads/minute per account/entry
with burst 3. At most 16 entry-store reads run concurrently, each with a five-second
context deadline; client cancellation propagates to the store. 429 and concurrency
503 responses supply `Retry-After: 5`. These are local load guards, not a durable
cross-replica quota. They do not replace deployment-wide request/authentication
limits. Clients should wait at least five seconds and back off on failures.

### Source check progress

The additive `source-status` route leaves existing entry/request responses
unchanged. It shares the per-entry polling budget with the entry-ID route and
the handler's 16 concurrent reads/five-second deadline. The existing read flag
is sufficient; delivery/admission need not be enabled to read history after a
worker rollback. Unsupported storage returns 503, never invented progress.
Neither capabilities nor this endpoint is connected to a new UI in this slice.

The response contains only `schemaVersion: 1`, `entryRequestId`, and
`sourceStatus` with `stage`, `attempts`, `maxAttempts: 8`, `observedAt`, and the
optional fields described below. Times are UTC; `observedAt` comes from the
database in the same statement as the committed ledger observation. These are
transport-probe attempts, not the later three-round automatic repair budget.

| Stage | Meaning and optional fields |
| --- | --- |
| `waiting_runtime` | No background attempt recorded. Does not prove the Bot is offline or the worker enabled. |
| `probing` | A source-probe lease is unexpired at `observedAt`; it does not prove socket delivery or ongoing execution. |
| `recovery_wait` | The saved lease has expired, including an expired eighth attempt. Only a worker can reclaim or durably settle it; a read does not change the ledger. |
| `retry_wait` | A saved retryable timeout/unavailable receipt; includes allowlisted `code` and `retryNotBeforeAt`. This is the earliest eligible time, not a guaranteed retry time or current authority check. It may be in the past while the runtime/worker is unavailable. |
| `verified` | A historical read-only source receipt, with `verifiedAt`. Not a captured candidate, validation result, upload, publication, or current execution permission. |
| `needs_user_action` | A saved terminal source failure or exhausted eight-attempt budget, with allowlisted `code`. No retry/reset button or auto-repair is introduced. |

Codes are limited to `publication.runtime_timeout`, `publication.runtime_unavailable`,
`publication.source_missing`, `publication.source_changed`, `publication.source_unsafe`,
`publication.runtime_unsupported`, and `publication.delivery_exhausted`, in their
matching stages. Unknown state/code combinations, inconsistent counters or
missing evidence return a sanitized 503 instead of being passed through.
No free-form messages, publisher UID, source references, device/body/installation,
lease token, lease deadline, node, connection or connection generation are exposed.

Original active human publishers retain historical reads after Bot ownership or
binding changes; new owners/friends do not inherit them. Account disablement is
checked again by the database query. This route does not check or grant current
runtime authority, clear a terminal failure, reset attempts, create another
intent, or trigger a model. An entry's orchestration state remains
`waiting_runtime` even after `verified`; only a future Cloud result can claim
publication. Clients must not present this observation as a live online/permission
indicator or fabricate a repair action from infrastructure errors.

## Workspace source verification and entry admission

### Durable admission before probing

New orchestration clients should use `skillhub.publication.createEntry.v2` when
`workspaceDurableCreateEnabled` is true in the capabilities response. The original
`createEntry.v1` below keeps its probe-before-admission contract; neither protocol
is connected to the production publishing UI yet. v2 requires all three existing
read/workspace/delivery flags, all default off, and a supported durable adapter.
Capabilities describe configuration/support, not worker or runtime health; the
RPC also refuses admission when the local delivery worker is not running.

The v2 request uses the same exact three-field payload, request ID and owner/device
headers as v1. It first authenticates the current human/Owner and verifies the
active, exact, same-node Bot/device/body/installation and connection generation.
Then it saves the immutable entry and reference-only outbox **before sending any
source probe**. Browser source references are still unverified selectors at this
point, not permission to read or upload files. Only the existing durable delivery
worker sends the read-only probe, with its fresh authority/connection checks,
leases, bounded retries and terminal source-failure handling.

The private final RPC result has exactly `schema_version: 2`, `entry_request_id`,
`state: "waiting_runtime"`, and `replay`. It means **durably accepted, not source
verified or published**. There is no provisional success ACK or transient probe
to the Bot on this path. Source progress is read using the existing private
`source-status` route. A missing/changed/unsafe source is a durable worker result,
not a reason to erase the admitted request or trigger automatic format repair.

Admission runs outside the WebSocket read loop, with at most 16 in-flight
admissions per Hub/process, no waiting queue, the shared v1/v2 account rate limit,
and a maximum ten-second context shortened by caller expiry or worker shutdown.
The database's transactional authority, capacity, quota and idempotency checks
remain in force. No new schema or production environment configuration is added.

An exact same-key retry after a legitimate reconnect preserves the original
saved connection generation if all other immutable identity/source fields match
and the current trusted generation has not regressed. It rechecks current
authority and does not reset the outbox, attempts or terminal outcome. Changed
Bot, device, installation, body, local selector or workspace generation conflicts;
new keys are never invented automatically. On an uncertain DB outcome or lost
response, use `/requests/{requestId}` with the original publisher login. A
concurrent request can still commit, so a transient 404 is not proof of failure.

After commit, a Bot disconnect does not retract admission. Loss of the publisher's
socket suppresses the private reply but does not cancel the durable intent; a
new login retrieves it through the private read API. The worker can recover it
on a different node after the same runtime reconnects, even when no first probe
was sent. Account/Owner/body changes still prevent new execution; original active
publishers keep private historical reads after transfer.

**Boundary:** a brand-new request still needs an online, same-node, trusted runtime
binding to establish its immutable source identity. v2 does not accept arbitrary
offline/cross-node browser selectors. Durable offline first admission still needs
a separately authenticated source-binding mechanism. This change does not
capture files, upload, delegate credentials, create repair conversations, invoke
models, bill accounts or change the legacy publishing buttons.

### Original probe first protocol

This protocol is an integration foundation, not a ready publishing button. Both
`CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED` and
`CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED` must be `1`/`true`.
Both default off. The adapter must implement durable entries, private request-ID
recovery and durable Bot connection generations; no in-memory generation fallback.
No production environment is changed by this implementation.

An authenticated, active human on an ordinary owner WebSocket sends a
`thin_tool_rpc` request with tool `skillhub.publication.createEntry.v1`, an
account-scoped `request_id` matching `[A-Za-z0-9_-]{8,128}`, the canonical
`target_owner_user_id` (`usr` plus the actor UID), and `target_device_id`.
The payload has exactly three string fields:

```json
{"bot_uid":"42","local_skill_id":"local-skill-1","workspace_generation":"workspace-v1"}
```

Bot UID is a canonical positive decimal string. Local references are bounded to
128 characters and validated by the entry contract; paths, URLs, source bytes,
extra identity fields and caller-provided generations of connections are rejected.
Chat, Wiki (including an owner Wiki login), service, Bot and Connector initiators
are not allowed. Friendship and Skill visibility do not authorize publication.

CatsCo checks active actor and Bot, current enabled Bot ownership, the exact
durable body binding, active registered device, installation, runtime role,
current Bot-authenticated socket, unexpired body lease and positive durable
`BotConnectionGeneration`. The durable generation must match this node's accepted
connection generation. Neither a legacy body nor DeviceID-as-BodyID fallback is
allowed. A desktop works only when already bound to that Bot through its Bot
socket, not a generic owner device. No selection invokes Bot switch or rebinding.

The runtime must explicitly advertise `skillhub.publication.sourceProbe.v1`.
Legacy share capability is not enough. CatsCo sends a fresh server-random probe ID
with the same three source selectors and a maximum 30-second expiry. The probe is
read-only: the XiaoBa adapter must verify the scoped local reference and workspace
generation without executing, repairing, finalizing, copying or uploading the Skill;
invalid manifest metadata must not be mistaken for an unsafe local source. The
adapter is not implemented by this CatsCo PR and must not advertise the capability
before it actually implements those checks. Browser/model callers cannot invoke
the reserved probe directly through thin-tool RPC.

Only the matching still-current Bot socket can answer the probe. A success result
has exactly `schema_version: 1`, `local_skill_id` and `workspace_generation`, with
references matching the pending selectors. This is a source-existence/generation
receipt, **not** a candidate digest, publication approval or bearer credential.
Unknown fields or mismatches are rejected; controlled local errors use
`publication.source_missing`, `publication.source_changed`,
`publication.source_unsafe`, `publication.runtime_unsupported` or
`publication.runtime_timeout`. Runtime `INVALID_REQUEST`, `OWNER_MISMATCH` and
`DEVICE_MISMATCH` map to `publication.runtime_unsupported`; `REQUEST_EXPIRED`
maps to `publication.runtime_timeout`. Other runtime errors, including
`BOT_NOT_ACTIVE`, become `publication.source_unavailable` on this workspace
admission path; raw messages/files are never sent to the browser or persisted.
These errors do not trigger model repair.

Background delivery uses the same request-error normalization, with
`publication.runtime_unavailable` as its fallback. `BOT_NOT_ACTIVE` also covers
transient workspace switching in XiaoBa, so it remains retryable (`pending`),
not `needs_user_action`. Retries retain the existing backoff, attempt limit and
fresh authority/binding checks; this does not authorize switching Bots or
retrying indefinitely. Invalid request/owner/device errors are terminal, while
expiry remains retryable as `publication.runtime_timeout`.

After checking authority/binding again and claiming the probe once, CatsCo creates
the immutable entry plus inert capture outbox using the existing transactional
store checks. The success result contains only `schema_version: 1`,
`entry_request_id`, `state: "waiting_runtime"` and `replay`. An accepted RPC ACK
means only that source verification is pending, not that a durable entry exists.
There is no candidate snapshot or final Cloud publication in this result.

Admission preserves the original request key for retries. Store conflicts,
authority denials, rate/storage limits and infrastructure failures are distinct
`publication.*` errors. On an uncertain admission, lost reply or browser restart,
first read `/requests/{requestId}` with the original human login. A 404 while work
is still in flight does not prove it cannot commit: wait/retry with the same key,
never automatically mint a new intent. Original-publisher history remains readable
after Bot transfer or runtime reconnect, even when a privileged retry is blocked.
The read lookup uses the existing account/request unique index and needs no DDL.

Probes are transient, rate limited per process/account (20/minute, burst 3), and
use existing bounded per-requester/device pending slots. Timeout or socket
replacement returns a safe error with the browser's original request ID; it does
not create a durable task. A process crash before admission requires retry. Once
admission commits, the entry and outbox survive even if its response is lost.

This slice intentionally requires browser and Bot sockets on the **same CatsCo
node**. Shared-runtime routing to another node fails closed with
`publication.route_unsupported`, because thin-tool pending replies are node-local.
It must not be enabled across replicas until durable routing/receipt handling is
implemented. This is not a change to legacy routing or a promise of cross-node
or offline intake support. Recheck authority, exact binding and source generation
again before future capture/upload; the probe is not a lock on future file content.

## Durable background source verification

`store.SkillHubPublicationDeliveryStore` adds an **internal** source-probe
delivery ledger to the existing outbox. It is the persistence prerequisite for
cross-node recovery of an **already accepted** entry, not cross-node/offline
admission. The PR627 browser protocol above remains same-node and online-only.
The worker is gated by `CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED` AND both
existing read/admission flags. All default off; no deployment configuration is
changed. PostgreSQL entry/delivery/generation interfaces and the scoped runtime
lister are required; unsupported adapters do not fall back to memory.

An internal scheduler can list at most 32 due entry IDs for one Bot. Claiming an
ID checks the original actor, current active human/Bot, enabled ownership/body
binding, immutable device/installation/workspace/local reference, and the current
durable Bot connection generation. Account/config/generation rows stay locked
through the short DB mutation. A reconnect can advance generation, but cannot
substitute any source selector. Neither the caller's observation nor a saved
record independently authenticates a socket: the node-local worker verifies
the exact Bot socket, body lease and explicit capability immediately before
dispatch and before accepting a receipt. These methods must never be exposed as
raw browser/runtime endpoints or called with model-supplied identity.

Each claim persists a random fencing token, node, connection, generation, attempt
count and a 30-second lease using database time. An active lease cannot be stolen;
after expiry another authorized node may claim with a new token. Stale, expired,
wrong-socket or wrong-generation receipts cannot settle a newer attempt. The
fence protects database receipts only; it is not an upload grant, file-access
permission or a guarantee of exactly-once external execution.

Only controlled timeout/unavailable results retry, with durable backoff of
5/10/20/40/80/160/300 seconds. At most eight claims are allowed per entry. Exhaustion
settles to internal `needs_user_action`, including recovery of a crashed eighth
attempt. No retry counter reset or automatic new entry is provided. Missing,
changed or unsafe source and unsupported runtime also require user action;
unknown/raw errors are rejected, not persisted or sent to a repair model.

A successful result records internal `source_verified` and its timestamp; an
identical receipt can replay after a lost acknowledgement without extending the
lease or changing the result. This is only a read-only source receipt. It does
not capture bytes, consume/delete the original event, issue a grant, upload,
install or publish. The immutable entry and public status remain `waiting_runtime`.
Future capture dispatch needs its own explicit transition and fresh authority
checks; it cannot treat this timestamp as current permission. The private
`source-status` projection above exposes historical progress, not a resume action.

Migration `000030_skillhub_publication_delivery` mirrors the startup DDL. It adds
columns/defaults/constraints/indexes without rewriting old identities or Bot
configuration. Rollback may remove the new columns only when no delivery attempt
exists, preserving old entries/outbox; once an attempt exists, rollback refuses
to erase fencing or receipt evidence. Stop consumers and back up before rollback.
Migration tests use frozen migration 000028 plus the existing relevant-table
user/Bot fixture and an actual pre-column intent/outbox, not a production dump.
Empty SQL, historical SQL-first/startup-first, repeated startup/migration, safe
rollback/reapply and refusal after a delivery attempt are covered.

### Scheduling and receipts

The server starts this worker only after choosing its final Hub and cancels it
on shutdown. A startup sweep and five-second ticks consider only locally
connected, registered Bot runtimes. Each sweep rotates through at most 16 Bots,
fetches at most four due IDs per runtime, and admits at most eight probes; each
node retains at most 32 pending probes. SQL filters the exact owner/Bot/device/
body/installation **before LIMIT**, so obsolete installations cannot starve
current work. Offline or unsupported runtimes consume no new claims. A process
restart or a move to another CatsCo node recovers accepted work from PostgreSQL
after the previous lease expires, using the same immutable source binding and
the newly verified durable connection generation. No Owner socket is required.

Dispatch uses the existing read-only `sourceProbe.v1` contract and a fresh random
`pubdelivery_` wire ID, separate from the database fence token. The token and
entry ID are not sent to the runtime. A second binding check runs after claiming
and immediately before local delivery. No source selectors are forwarded through
the shared-runtime relay. The socket queue is nonblocking; background backpressure
does not disconnect a Bot or perform database work on its WebSocket read loop.

Reserved receipt IDs bypass the legacy requester-bound router and its generic
logs, including late/unknown IDs. Only the exact registered Bot connection can
enqueue a receipt. The strict three-field schema and optional routing headers
must match; mixed/error payloads cannot claim success. Raw messages are discarded,
unknown errors become retryable runtime-unavailable, and only normalized outcomes
enter a bounded queue served by two workers. Fresh runtime binding checks and
the transactional owner/generation/fence checks precede durable settlement.
Concurrent receipts enqueue once; overload returns a safe acknowledgement without
consuming the pending receipt. After completion, a duplicate wire response may
return 404; it cannot repeat work or create another entry.

The probe deadline is five seconds before the 30-second DB lease deadline, leaving
time to persist timeouts. A one-second sweep queues expired probes independently
of scheduling; receipt DB calls have five-second contexts. If a write fails or its
outcome is uncertain, the worker never fabricates success or creates a new intent:
a committed result remains durable, otherwise lease expiry allows bounded retry.
Only the read-only probe may be retried; the fence is not an exactly-once guarantee
for future side effects. Existing authority lookup APIs remain synchronous in
the background workers, never in the WebSocket receipt admission path.

The real-PostgreSQL dispatcher test uses two independent Hubs and a synthetic wire
peer: it covers owner absence, node loss, lease recovery, stale replies, immutable
intent and obsolete-device starvation. It is not a live XiaoBa or Redis deployment
test. XiaoBa still must implement and explicitly advertise the new capability.

## Remaining integration

Durable **pre-probe** admission, UI integration of delivery progress, safe resume, and the
later capture/upload adapter remain required. Initial browser submission is still
same-node and online-only. The worker resumes already accepted requests, not new
offline requests. No production switch should be enabled on these tests alone.

There are no snapshot/upload dispatchers. Existing workspace
buttons, chat publishing, Bot binding, model switching and private backup paths
are unchanged. Enabling status reads alone does not activate entry admission.

Chat inputs are rejected until a trusted live-turn grant and owner-private topic
authorization are implemented. The current schema supports only the initial
workspace state; later lifecycle changes require explicit schema evolution,
compare-and-swap and crash-recovery tests, not arbitrary state strings.

The rest of contract stage 1 remains: durable cross-node/offline intake, chat
authorization, short-lived scoped grants, authenticated introspection,
cancellation and commit-permit ordering. XiaoBa source-probe and capture adapters,
Cloud upload and the workspace UI still need to be connected and jointly tested.
Repair conversations, automatic formatting repairs, approvals, model billing,
Chinese guides and frontend redesign are not part of this change.

## Verification

Pure store tests cover bounded inputs, policies and immutable identity fields.
HTTP tests use real signed JWTs and adapter doubles to cover active-human-only
access, repeat account checks, privacy/projection, strict routes and transports,
default-off capabilities, unsupported adapters, poll limits, deadlines and
concurrent-read bounds. The CI race-sensitive group includes these HTTP tests.
PostgreSQL integration tests cover persistence across Adapter instances, schema
reinitialization, private reads, current authority on replay, outbox rollback,
concurrent duplicate requests, quota races, owner-transfer races, rate limits
and cancelled request contexts. CI runs the new integration group against its
isolated PostgreSQL service with the Go race detector:

```sh
go test ./server/store -run SkillHubPublication -count=1
go test -race ./server -run '^TestSkillHubPublicationRead' -count=1
# CATS_PG_TEST_DSN must point to a disposable test database.
go test -race ./server/db/postgres -run '^TestPostgresSkillHubPublication' -count=1 -v
```

Without `CATS_PG_TEST_DSN`, database tests skip; this is not evidence of database
correctness. User manual testing waits for the XiaoBa source-probe adapter and
workspace button to be connected in an isolated environment. Do not enable Cloud
publication switches based on this PR alone.

Migration verification uses a frozen **relevant-table** baseline from pre-PR620
main `bf2e938d`: `users` and `bot_config`, plus synthetic owner/Bot rows. This is
not a production database dump or a claim to test every historic production table.
The fixture contains no publication tables. Tests cover empty SQL creation,
historical startup-first and SQL-first upgrades, repeated startup/up execution,
preservation of the legacy rows, and working admission after either order.

The `down` migration atomically removes only empty publication tables, in reverse
dependency order, without CASCADE. Retained entries/outbox rows make it fail
closed, preserving replay evidence. Tests verify empty down/up and rejection of
populated rollback. Stop writers, back up and deploy a compatible older binary
before an actual rollback; otherwise current `CreateSchema()` recreates the
tables. No migration is executed on production by this PR.
