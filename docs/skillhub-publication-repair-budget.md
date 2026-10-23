# Publication repair budget ledger

This is internal, optional accounting for the publication repair workflow. It has
no registered HTTP/RPC handler, worker, model tool, task event or file writer.
Ordinary publication and existing transport retries do not call it. Only the
PostgreSQL adapter implements the interface; there is no in-memory fallback.

## Admission and authority

A future trusted adapter must fetch and verify the exact rejected Cloud report,
match its candidate to the retained upload receipt, verify the original human's
explicit repair action, and verify a private `agent_task` owned by that human
whose sole Bot is the original Bot. The topic string and evidence digests in this
store are references, not proof of those facts. Never expose these store inputs
directly to a browser or model.

The ledger binds one immutable task to each publication entry, receipt, topic and
initial report. An identical create returns its existing history; changing facts
conflicts. Every write locks the publication entry and rechecks the active human,
current Bot owner, body, enabled state, cancellation and commit intent. This is
the same serialization point used by cancellation. Only the original active
human can read history, including after cancellation or Bot transfer. Reading
history grants no authority to resume.

## Three rounds, retries and recovery

Claiming a new round durably spends one of three slots before work starts. Its
request ID and input evidence are immutable. Replaying a completed request
returns its checkpoint; replaying a live claim by the same worker returns the
same lease. A competing worker cannot claim a live round. A new request cannot
skip a pending round, including after its lease expires.

An expired claim can be recovered only with the same request and evidence. It
keeps its round number and receives a new token and generation, fencing the old
worker. **A lease is not permission to repeat an uncertain paid model call.** A
future executor must reconcile durable model-call/checkpoint state first, and
stop with `model_result_uncertain` if it cannot establish what happened. That
executor and its call checkpoints are not implemented here.

Transport retry budgets remain independent. Recreating an adapter or rerunning
schema startup preserves all rounds. Tables retain immutable tasks/checkpoints;
the down migration refuses to erase a nonempty budget.

## Checkpoints and stopping

Each round consumes evidence from the exact preceding report. Evidence includes
the candidate digest, exact verified report digest, and normalized issue-set
digest. The issue-set digest must exclude fresh report IDs and display prose.
If both candidate bytes and normalized issues are unchanged, a new report ID
does not count as progress. Rejected results with progress may reserve the next
round, until round three. No progress, budget exhaustion, explicit safety/identity/
semantic/runtime/uncertainty stops, `validated`, and `waiting_confirmation` all
prevent a new round.

`validated` means only that candidate validation passed, not that publication
occurred. `waiting_confirmation` cannot be unlocked through this interface.
Exact human approval of a description draft is separate future work. The ledger
does not decide whether a reported issue is safe to repair; a future executor
needs an explicit server-controlled format allowlist, independent of model text.
Neither a lease nor a checkpoint authorizes editing a runtime Skill, changing
tool permissions or modifying model prompts.

## Verification

The store unit tests cover admission evidence, digest validation and stopping.
PostgreSQL integration tests cover restart/reclaim, exact replay, three-round
exhaustion, concurrent claims, cancellation races, changed authority, private
history, stale workers, direct SQL budget guards and retained schema startup.
They require `CATS_PG_TEST_DSN`; the existing publication CI job provides a real
PostgreSQL service and runs the recovery/concurrency cases with the race detector.
