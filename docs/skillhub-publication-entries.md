# SkillHub publication entry persistence

This is the first implementation slice of the
[approved three-repository contract](https://github.com/buildsense-ai/catsco-skillhub-cloud/blob/c369cc9d5e956a4c42095ebcbb93b4ac7506e5f0/docs/api/publication-entrypoint-delegation.md).
It adds a PostgreSQL store for durable **workspace publication intents** and a
default-off private status API, not publication, delegated credentials or a new
user-facing publishing entrypoint.

## Implemented behavior

The optional `store.SkillHubPublicationEntryStore` creates an immutable entry and
a reference-only `capture_candidate` outbox row in one transaction. No worker
consumes this event yet. Creation always returns `waiting_runtime`, never a claim
that a file was captured, validated, uploaded or published.

The database checks an active human account, an active enabled Bot owned by that
human, and the Bot's durable body binding. These rows stay read-locked through
insertion, serializing account disablement, owner transfer and body changes
against admission. This is necessary but **not sufficient** runtime authorization:
the future trusted caller must authenticate the human and verify device,
installation, route generation, workspace generation and local Skill reference
against current server/runtime facts. Caller-provided UIDs or copied record fields
are not authority. This store must not be exposed by blindly decoding an HTTP body.

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
(case-insensitive, trimmed) enables reads. There is no creation flag or endpoint.
The handler authenticates an active CatsCo human JWT from one `Authorization:
Bearer ...` header itself, including a fresh account-state check. It accepts no
query credentials, API keys, cookie-only sessions, caller UID, query parameters,
request body or arbitrary source path. A display name is never an identity key.

| Route | Behavior |
| --- | --- |
| `GET /api/skillhub/publications/capabilities` | Available to authenticated humans even with reads off. Returns `schemaVersion: 1`, effective `entryReadsEnabled`, `workspaceCreateEnabled: false`, `chatCreateEnabled: false`, and `minPollIntervalMs: 5000`. Adapter support is not a database health check. |
| `GET /api/skillhub/publications/entries/{entryRequestId}` | When enabled on a supported store, reads the original publisher's record. Missing, wrong-publisher and disabled reads return the same 404. Bot ownership transfer does not grant historical record access to the new owner. |

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

## Deliberately not enabled

There are no HTTP/WebSocket creators, runtime dispatchers or outbox consumers.
Existing workspace buttons, chat publishing, Bot binding, model switching and
private backup paths are unchanged. Enabling status reads does not activate them.

Chat inputs are rejected until a trusted live-turn grant and owner-private topic
authorization are implemented. The current schema supports only the initial
workspace state; later lifecycle changes require explicit schema evolution,
compare-and-swap and crash-recovery tests, not arbitrary state strings.

The rest of contract stage 1 remains: trusted entry adapters, runtime route
validation, short-lived scoped grants, authenticated introspection, cancellation
and commit-permit ordering. Then Cloud and XiaoBa adapters can be connected.
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
correctness. This change needs no user manual testing because no publishing
entrypoint is exposed yet. Do not enable Cloud publication switches based on this
PR alone.

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
