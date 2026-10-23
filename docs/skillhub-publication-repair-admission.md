# Human publication repair admission

This adds trustworthy admission to the existing three-round repair ledger. It is
not an execution endpoint, and the browser handoff has not been wired to it yet.
No model, candidate write, ordinary file/shell/Skill tool, system prompt or
publication authorization changes in this step.

## Default off

Both `CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED` and
`CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED` must be explicitly enabled.
The latter defaults to false. Existing publication peer settings and PostgreSQL
are required; missing support fails closed. This change does not enable flags in
production or change Bot deployments.

The authenticated human commit handler accepts:

- `POST /api/skillhub/publications/commit/entries/{entry}/repair-task` with exactly
  `{"schemaVersion":1,"topicId":"grp_123"}`.
- `GET` on the same path to recover the existing task's safe projection, with no
  body or query. This is a read only: no new task, Cloud call or budget spending.

Bearer authentication uses the existing active-human JWT checks; Bot tokens,
cookie-only requests, unknown/duplicate fields, noncanonical topics, extra
evidence, queries and encoded bodies are refused. Existing per-human/per-entry
rate limits, concurrency slots, response privacy and timeouts apply.

## Trust and atomicity

POST reads the original publisher's current workspace publication context and
retained receipt, calls only the pinned Cloud report API, validates the exact
receipt and candidate, and rejects passed or truncated reports. It rechecks the
entry identity/source/receipt/authority after peer I/O. No database locks are
held across the network call. The browser cannot submit candidate/report/issue
hashes, repair policy, a worker identity, a lease or model output.

The server computes a digest of the exact verified report and a separate sorted,
deduplicated issue identity digest. Issue identities contain code, category,
blocking status, bounded location and repair policy, not report IDs or display
messages. Cosmetic report changes cannot create apparent repair progress.

In the create transaction, PostgreSQL locks the publication entry, rechecks its
original active human, workspace entrypoint, Bot owner/body/enabled state, exact
receipt, cancellation and commit intent. It also checks the canonical group
topic, group owner and `agent_task` kind, topic owner/type, and exact membership
(the original human in the owner role and the original Bot, nobody else).
The group is locked `FOR UPDATE`, blocking new FK-backed member inserts; current
member rows and the topic are locked to fence removals and edits. These checks
also run for claim/reclaim/replay and finish, not just initial task creation.
Concurrent group/entry operations may time out and fail closed; there is no
unlocked fallback.

An identical admission returns the retained task without spending a round.
Changing topic or report evidence conflicts, including after a lost response;
GET recovers the original association. There is no rebind/reset operation. A
later task membership change invalidates execution writes; it does not delete
the original human's private ledger history or refund spent rounds.

## User-visible projection and remaining work

Replies expose only schema/entry/Bot/topic/task identifiers, state, rounds used,
the three-round limit and `automaticRepairEnabled:false`. No internal receipt,
report/candidate hashes, lease token, worker identity or report is serialized by
this endpoint. GET requires current publication authority; lower-level private
ledger history remains readable by its original active publisher after transfer
or cancellation, but that is not execution authority.

Admission alone does not approve a description, make a format issue automatic,
spend a round, start a worker, call a paid model or publish anything.
[Exact human draft confirmation](skillhub-publication-description-confirmation.md)
is separately retained; it still does not execute or resume a repair. Restricted
execution with durable model-call reconciliation and browser/original-chat
continuation remain subsequent work. The runtime must
still recheck the precise retained source before any repair; this API's stored
report is not permission to read or edit live files.

## Verification

Unit tests cover human authentication, strict request decoding, default-off and
unsupported stores, rate/concurrency limits, private projection, report binding,
post-network authority changes, normalized issue hashing and exact replay.
Real PostgreSQL tests cover invalid/changed task ownership/type/membership/topic,
no partial task on rejection, retained budgets and row/FK locking of concurrent
member inserts/removals and kind changes. The existing required JWT/HTTPS/PG
contract verifies admission survives a lost response and reconstructed handler,
replays the same zero-round task, and rejects rebinding and changed task kind.
Its Cloud rejection is explicitly synthetic, not full live repair acceptance.
