# Session Fork Server Implementation Plan

> Execute in the current dev-build stage. The approved spec is authoritative.

Goal: durable, authenticated, idempotent server half of a real session fork.

Architecture: shared SQL transaction implementation behind an optional store
interface, with PostgreSQL/MySQL adapters. Topic revision/write-state columns
and message triggers enforce the fence for every database writer. Existing
bot socket delivers control only; runtime acknowledgement unlocks the target.

Tech Stack: Go, database/sql, PostgreSQL, MySQL.

Spec: D:/codex_workspace/session-fork-task/spec.md (approved r2).

## Constraints
- Only server/**; no commit, push, deployment, or live-data operations.
- Same owner and bot, p2p or two-member agent_task only; no A2A or model calls.
- Preserve content, author, timestamp, attachments; new IDs and remapped replies.
- No historical fanout. Pending/failed targets reject all message writes.
- Existing wire contract is unchanged; runtime owns the authoritative idle gate.

## Tasks
- [x] Store: add fork types/interface, transactional creation, user-scoped request
  identity, completion and query. Test duplicate/conflict, ownership, revisions,
  complete/failed transitions and rollback through the store interface.
- [x] Schema: install durable fork table, topic revision/write state and message
  triggers on both adapters; compare migration files with startup SQL.
- [x] HTTP/socket: JWT create/get, API-key completion, existing bot socket control,
  online/status gate and pending write fence. Test auth and both ingress paths.
- [x] Verify: focused Go tests, affected suites/build; record attachment retention
  findings, candidate identity, actual results and remaining gaps in handoff.

## Review Focus
- Concurrent source input/edit/delete must invalidate completion, even on restart.
- Duplicate requests and acknowledgements never copy twice or overwrite progress.
- GET cannot disclose another user's fork; bot cannot complete another bot's target.
- Pending and failed target fences must include transient and stream publishes.
- Source deletion must not remove copied messages or uploaded attachment bytes.

## Decisions
Ruling: use a shared dialect-aware SQL implementation, not duplicate transaction
algorithms; both existing adapters use database/sql and differ only in SQL syntax.
Ruling (supersedes initial timeout proposal, per parent): no pending expiry.
GET is read-only. Pending targets stay blocked and can retry after reconnect;
source revision and body binding still gate completion. Failed IDs are terminal;
an intentional new attempt needs a new request ID.
Later amendment (ack-timeout follow-up): the no-expiry ruling assumed every
runtime acknowledges. A runtime built before session forks ignores the control
frame silently, so the copy stayed pending forever and the client waited on it
indefinitely. Pending copies now expire to `failed`/`session_fork_ack_timeout`
after CATS_SESSION_FORK_ACK_TIMEOUT_SECONDS (default 120), and the user may
close a pending copy explicitly via DELETE with `session_fork_cancelled`. Both
paths are conditional on the row still being pending, so a concurrent
completion, cancel or a second reaper wins and a terminal state is never
rewritten. A healthy runtime still acknowledges in seconds, so the window does
not change the successful path.
Ruling: revision triggers cover inserts, updates and deletes, including writers
outside the normal HTTP/WebSocket handlers. Target state is stored on topics so
the same row lock linearizes clone completion against writes.

Review repair SR1: expose persisted remapped replies through Message and HTTP/WS
history, and align all shared scanners (including around/debug readers). MySQL
around now reuses its existing common scanner instead of a duplicate decoder.
Focused tests first reproduced the missing reply/column projections, then passed.
Actual PostgreSQL transaction tests supplement MySQL SQL mocks. See
server-handoff.md and candidate.sha256 for final evidence and candidate identity.
