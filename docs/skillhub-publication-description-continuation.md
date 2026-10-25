# Continuing an exactly confirmed description

The internal `continueConfirmedPublicationDescription` operation connects the
existing exact human confirmation, deterministic private application, durable
passing-report acceptance, and retained-copy recovery. It returns one private
handoff containing an authoritative checkpoint and verified bytes. It is not
registered as a production route, RPC, worker, model tool or publishing action.

## Recovery and history

The operation reuses the original application identity and first accepted
report. A lost application-completion or report-acceptance reply is recovered
from PostgreSQL on the next invocation. A completed application skips derivation;
an accepted report skips a fresh report request. Reading private bytes remains
necessary on every handoff, so an expired or missing Cloud copy fails closed.
Deterministic retention replay is not permission to retry a paid model call.

No new table or migration is needed. The read-only
`GetSkillHubPublicationDescriptionContinuation` transaction assembles the repair
task, exact confirmed draft, completed application and immutable acceptance
under the existing current entry/human/Bot/body/private-task locks. It also
rechecks the original retained receipt, cancellation and commit state. Missing,
incomplete or inconsistent evidence fails without constructing a handoff.

The complete round chain must be consistent, ordered and within the three-round
budget. Earlier rounds must have made progress and permitted another round.
The last round remains `waiting_confirmation`; the separate accepted report says
that the derived candidate passed validation. Do not replace the historical round
with `validated`, create another round for this continuation, or reset the budget.
No public task/UI state changes in this implementation.

## Current checks and private bytes

After application and acceptance recovery, the coordinator reads the complete
transactional checkpoint. It brackets a retained-copy read with original Runtime
source verification and identical current checkpoint reads. Copy length and
SHA-256 must match the retained derivation result, whose candidate/limits are
bound to the accepted report. Original source and derived private copy are
different identities and must never be compared as if they were the same bytes.

Nested application, draft, report, round-result pointers and private byte buffers
are copied. Changes by a subsequent port invocation cannot silently change an
earlier baseline or a returned handoff.

Runtime/Cloud observations and PostgreSQL transactions are separate operations;
this is not a distributed atomic authority check. The returned object is a
point-in-time handoff, not a long-lived execution or commit grant. The release
consumer still needs current source/authority checks and a commit protocol bound
to the derived candidate. The existing original rejected-candidate commit path
must not be reused by substituting hashes or replacing its receipt.

## Verification and remaining work

Domain tests cover exact task/draft/application/report binding and earlier-round
progress. Coordinator tests cover recovery at committed checkpoints, stable
replay, source/authority changes, invalid bytes, context cancellation and pointer
isolation. PostgreSQL tests cover missing acceptance, restart recovery, current
authority, unchanged history/budget and cancellation.

The existing mandatory JWT/HTTPS/PostgreSQL publication contract now continues
after real exact human confirmation. It loses replies after real application and
acceptance commits, reconstructs the coordinator wrapper, verifies recovery and
checks task-authority changes during copy recovery. Cloud derivation/report/copy
and Runtime source are synthetic ports. These are not real Cloud/Runtime/PG or
paid-model end-to-end tests. Local database skips are not PostgreSQL passes.

M3 still requires the derived-candidate release/commit protocol, later-round
private candidate ancestry/derivation, uncertain paid-call reconciliation and
both publication entry points. Production wiring/enabling and user acceptance
remain later steps; Chinese presentation content and author images remain after
the publishing-repair loop.
