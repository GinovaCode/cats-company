# Session Fork Server Handoff

Date: 2026-10-19. Spec: D:/codex_workspace/session-fork-task/spec.md r3, with
applicable builder-check.md refinements and parent review clarification to avoid adding an unrequested expiry.
Repository: D:/codex_workspace/cats-company-session-fork.
Baseline HEAD: d660ab03163ab417e96e998d42d6a9eb23eb5569.
Uncommitted server candidate is frozen and independently reviewed by Bohr.
Historical candidate contents: [candidate.sha256](candidate.sha256); these hashes
predate the PR #671 follow-up fixes below.
No webapp/XiaoBa edits, commits, pushes, deployment or existing-data reset.

The user-confirmed r3 scope copies the source's existing cloud history and actual
persisted local context as two representations, kept stable during copying. It
does not audit input coverage or repair pre-existing source synchronization gaps.
The completed-sequence certificate is no longer required. Independent R1 concerns
a gap already present in the source, not context lost by the clone; it is distinct
from the server reply-reader repair SR1 below. Missing local context still fails
closed, without substituting cloud text. This r3 update changes documentation
only: Go/SQL and the frozen candidate hashes are unchanged. No new database test
run was performed; the verification and review results below are retained evidence
from the previously tested server candidate, not a new r3 runtime acceptance run.

## PR #671 follow-up fixes

- PostgreSQL uses `000042_session_forks` (up/down); latest main at `b987e59a`
  owns `000041_skillhub_publication_commit_deliveries`. MySQL filenames retain
  `000041_session_forks`. The PostgreSQL DDL itself is unchanged.
- Stale cloud task status permits dispatch to the current local runtime for its
  locked idle/interruption checks. Running/waiting status still rejects creation.
- Necessary MySQL fork schema initialization errors stop startup in every
  environment, preserving the original error and administrator guidance.
  Unrelated non-production schema errors retain the existing logging behavior.

The verification results below describe the historical candidate, not these
follow-up fixes. No concurrent writers during PostgreSQL initialization and a
single-node fork rollout remain deployment requirements.

## Implemented

- JWT POST/GET/DELETE /api/conversations/fork, user-scoped request identity and
  durable pending/ready/failed provenance. Payload conflicts reject reuse.
  Terminal state wins before revision/online/body checks; failed never reopens.
  GET is read-only. A new attempt after failure requires a new request ID.
  DELETE cancels a pending copy (`session_fork_cancelled`); pending copies also
  expire to `failed`/`session_fork_ack_timeout` after the acknowledgement window
  (CATS_SESSION_FORK_ACK_TIMEOUT_SECONDS, default 120s) when the runtime never
  acknowledges, so an old or offline runtime can no longer leave a copy pending
  forever. See the ack-timeout amendment in plan.md.
- Same owner/bot only, canonical p2p or exactly two-member agent_task. Online
  body/status gate plus runtime idle/local snapshot and server revision checks.
- Serializable SQL clone creates a real agent_task group/topic and memberships,
  new message IDs, original timestamps/authors/content/blocks and safe metadata.
  Replies and annotation references are remapped transactionally. Client dedup
  IDs and runtime/credential authority are not copied. Default name: 会话副本.
- Existing socket receives only session_fork control, no historical fanout or
  synthetic chat turn. Server never invokes a model, tools or A2A for cloning.
- HTTP and WebSocket normal, transient, stream and task-status sends are fenced
  until ready. SQL triggers also guard direct inserts/updates while pending/failed.
  Message insert/update/delete increments topic revision under its row lock.
  Source or target changes before completion fail explicitly. Terminal ready
  retries remain ready even after later source progress/deletion.
- PostgreSQL/MySQL startup schema plus matching up/down migrations. Shared
  dialect-aware database/sql transaction implementation avoids divergent logic.
- SR1 repaired: Message carries reply_to, both history serializers expose it,
  and all shared adapter readers include metadata/reply columns (including
  around and PostgreSQL bot debug). MySQL around reuses its common scanner.

## Final Wire Contract

User JWT POST /api/conversations/fork:
`{source_topic_id, request_id, name?}`.
GET accepts exactly one of `request_id` or `topic_id`.
Response: `{request_id, source_topic_id, target_topic_id, state, error?}`.

Socket envelope `session_fork`:
`{request_id, source_topic_id, target_topic_id, actor_user_id, agent_uid, source_last_seq, message_id_map}`.
IDs/sequences use existing numeric wire convention; map keys are source ID strings,
values target numbers. Pending retry resends the same target/map, never recopies.
`source_last_seq` remains the copied cloud-history boundary; it is not a local
completion certificate or proof that every visible input was processed. The
server wire contract is unchanged in r3.

Bot HTTP auth: `Authorization: ApiKey <key>`.
POST /api/bot/session-forks/complete body:
`{request_id, target_topic_id, state: ready|failed, error?}`.
Pending completion requires `X-CatsCo-Body-ID` matching the recorded body and
current binding. Ready/failed terminal retries precede those checks.

GET /api/bot/session-forks?topic_id=... supplies bot-scoped restore provenance.
Only structured HTTP404 `{"error":"session_fork_not_found"}` proves not-a-fork;
other errors must fail closed. Headers, fields and terminal semantics remain as
coordinated with runtime worker Cicero. The r3 runtime contract uses an idle local
snapshot plus server revision checks, not a completed local/cloud sequence gate.
Missing local fork context must never be replaced with cloud display text.
Release server first, as documented by the parent's root runtime handoff.

## Attachments And Credentials

Copied blocks retain upload URLs and file descriptors. Existing upload bytes are
stored independently of topics; deleting a group removes DB topics/messages, not
those upload files. Copies provide their own message-based topic authorization.
File-open descriptor hashing is shared with the clone remapper so target topic,
message ID and descriptor hash agree. Missing/unmappable annotation bindings fail
the clone transaction rather than creating invalid authority.

The metadata sanitizer strips artifact/page context and known transient authority
keys, including catsco_identity, xiaoba_runtime, device grants/access tokens,
skill connectors, trusted annotation context and channel-delivery trust markers.
Raw JSON handling preserves unrelated metadata and numeric precision.
This is not filesystem or artifact-store isolation: the same user/bot may continue
to reference the same underlying bytes. PostgreSQL tests verify retained copied
file references after source deletion; existing file-open/hash tests also pass.

## Verification

Final command (PowerShell, repository root):

```powershell
$env:GOPROXY='https://goproxy.cn,direct'
$env:CATS_SESSION_FORK_PG_TEST_DSN='postgres://postgres@127.0.0.1:55441/postgres?sslmode=disable'
& D:/codex_workspace/.tmp/go1.26.6/go/bin/go.exe test -mod=readonly ./server/... -count=1
& D:/codex_workspace/.tmp/go1.26.6/go/bin/go.exe build -mod=readonly -buildvcs=false ./server/...
git diff --check -- server
```

All commands exited 0. Go auto-selected required Go1.26.9. Final test output:

```text
ok github.com/openchat/openchat/server                 32.720s
ok github.com/openchat/openchat/server/cmd              2.128s
ok github.com/openchat/openchat/server/db/mysql         0.717s
ok github.com/openchat/openchat/server/db/poolhealth    0.573s
ok github.com/openchat/openchat/server/db/postgres     16.213s
ok github.com/openchat/openchat/server/db/sessionfork   0.540s
ok github.com/openchat/openchat/server/store            0.557s
ok github.com/openchat/openchat/server/store/types      0.512s
```

Log: D:/codex_workspace/cats-company-session-fork/server/.session-fork-test/final-regression.log.
Focused SR1 tests demonstrated red/green for missing reply fields and around/debug
projections; the final full suite includes their green result.

Real isolated PostgreSQL16.4 tests cover schema reinstallation, complete clone,
reply/metadata/file preservation, request conflicts/scope, pending/failed direct
write fences, old pending read-only GET, wrong-body rejection, terminal retries,
source insert/edit/delete/topic deletion, source/target membership and body
changes, target mutation, concurrent source update versus completion, and eight
concurrent same-ID requests without duplicate groups. Each run uses a fresh
unique schema and drops only that schema. No existing or production DB was used.

MySQL SQL mocks cover transaction/copy, rollback, terminal-state ordering and
migration/startup parity. There was no available real MySQL engine; Docker's
dockerDesktopLinuxEngine pipe was unavailable. Actual MySQL trigger execution
and transaction concurrency remain an explicit verification gap.

Default Go VCS stamping failed while inspecting the parent workspace Git directory;
the explicit -buildvcs=false build passed. This is an environment workaround, not
a source or go.mod change. No live browser-to-XiaoBa end-to-end run was performed
by this worker; parent/runtime/frontend verification is separate.

## Local Infrastructure And Review

The isolated PostgreSQL distribution/data/logs are under the ignored
server/.session-fork-test directory. `git status --short -- server/.session-fork-test`
is empty; generated binaries/data are not part of the candidate. Files/logs are
retained rather than deleted. PG listens only on 127.0.0.1:55441, fresh trust-auth
test cluster. After Bohr confirmed completion, pg_ctl fast stop exited 0, the
original startup shell session exited 0, and port 55441 had no remaining listener.
No cluster data or logs were deleted; no service remains running from this task.

To start this existing isolated cluster for another check (do not initialize or
delete any data), use the following from server/.session-fork-test:

```powershell
Start-Process -FilePath (Resolve-Path ./bin/pg_ctl.exe) -ArgumentList '-D data -l postgres.log -o "-h 127.0.0.1 -p 55441" -w start' -WindowStyle Hidden -PassThru
```

To stop only this cluster after review:

```powershell
Start-Process -FilePath (Resolve-Path ./bin/pg_ctl.exe) -ArgumentList '-D data -m fast -w stop' -WindowStyle Hidden -Wait -PassThru
```

The current implementation requires the bound bot socket on this server Hub;
cross-node control routing is not added. An unavailable local socket fails closed.
Independent review report: D:/codex_workspace/session-fork-task/server-review.md.
Bohr's final verdict is a qualified server-scope pass, SR1 resolved, no remaining
confirmed code blocker. Independent full suite passed all eight packages
(server32.979s, PostgreSQL15.623s), build and diff check passed, and product/test/
schema hashes were identical before and after tests. Live MySQL remains the
disclosed verification gap. The temporary PostgreSQL cluster was stopped only
after that final verification and the reviewer's explicit release.
Any code repair after freeze must regenerate candidate hashes and rerun relevant
tests; self-verification is not independent acceptance.
