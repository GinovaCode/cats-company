# SkillHub publication entry persistence

This is the first implementation slice of the
[approved three-repository contract](https://github.com/buildsense-ai/catsco-skillhub-cloud/blob/c369cc9d5e956a4c42095ebcbb93b4ac7506e5f0/docs/api/publication-entrypoint-delegation.md).
It adds a PostgreSQL store for durable **workspace publication intents**, not
publication, delegated credentials or a new user-facing entrypoint.

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
operation. The API-layer authentication and safe response projection are still
to be implemented. The store's internal records must not be serialized wholesale
into chat, browser responses or logs.

## Storage and limits

Ordinary additive tables are registered in `server/db/postgres/schema.go`, following
the repository's migration policy. `CreateSchema` adds them idempotently on the
next normal startup. There is no separate numbered migration or runtime DDL in
store operations. Startup schema failures retain existing startup behavior;
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

## Deliberately not enabled

There are no HTTP/WebSocket routes, environment flags, production callers or
dispatchers for this store. Existing workspace buttons, chat publishing, Bot
binding, model switching and private backup paths are unchanged. The new tables
do not activate any of them.

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
PostgreSQL integration tests cover persistence across Adapter instances, schema
reinitialization, private reads, current authority on replay, outbox rollback,
concurrent duplicate requests, quota races, owner-transfer races, rate limits
and cancelled request contexts. CI runs the new integration group against its
isolated PostgreSQL service with the Go race detector:

```sh
go test ./server/store -run SkillHubPublication -count=1
# CATS_PG_TEST_DSN must point to a disposable test database.
go test -race ./server/db/postgres -run '^TestPostgresSkillHubPublication' -count=1 -v
```

Without `CATS_PG_TEST_DSN`, database tests skip; this is not evidence of database
correctness. This change needs no user manual testing because no entrypoint is
exposed yet. Do not enable Cloud publication switches based on this PR alone.
