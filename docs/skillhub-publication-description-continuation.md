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

## Planning the independent repaired release

`planConfirmedPublicationDescription` consumes this private handoff, validates
its bytes and full checkpoint, and brackets `PlanRetainedCopy` with current
transactional checkpoint reads and original Runtime source checks. The complete
task/draft/application/acceptance must remain identical. Port-owned pointers
cannot change the baseline or returned plan. This coordinator has only a
read-only store interface and is not registered as a production action.

The peer client requires the derivation configuration plus the separate
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_PLAN_ENABLED` switch (default false).
It derives `/internal/plan` exclusively from the configured HTTPS origin, clears
the cookie jar, refuses redirects, bounds request/response sizes and concurrent
calls, and accepts only exact noncacheable JSON. Original upload receipt,
application, complete derivation receipt, derived byte length, derived candidate
hash, target structure and requested version-policy boolean must all match.
Nested duplicates, null/missing fields, old-candidate targets and receipt
substitution fail closed. Errors never include upstream private bodies/URLs.

Cloud #55 retains one immutable independently reserved target, excluding the old
original-candidate plan for the same task. A lost reply is recovered by asking
for the same plan; neither side automatically retries a paid model call. The
description's human confirmation does not implicitly authorize a new version:
`confirmVersionPublish` is a distinct policy input, not human authorization.

The returned private checkpoint/plan is still a point-in-time observation, not
a durable CatsCo release intent, execution permit or publication success. No
CatsCo row, round, original Skill, task state or budget is changed. The old
commit path remains original-only. A subsequent step must durably bind the
accepted application/report/target and obtain a distinct candidate-bound permit
under current authority/source checks before publishing.

## Authorizing the retained derived release

After a separate durable release intent has been confirmed by the actor, the
internal authorize client sends only the original upload receipt and application
identifier to Cloud. Cloud recovers or issues a permit for the exact retained
derived target; it never creates a commit or publication job. CatsCo brackets the
call with current continuation, intent, identity, target, source and cancellation
checks. A permit is therefore an internal handoff, not permission to bypass the
future derived commit worker. The response is strict wrapper-plus-proof JSON:
the nested proof must match the application, intent digest, target and UTC
authorization time. Replays are immutable and pointer-isolated. Unknown,
cacheable, compressed, redirected, oversized, duplicated, null or mismatched
responses fail closed; upstream unavailable errors do not become a new intent.

The authorize client uses a separate default-off
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_AUTHORIZE_ENABLED` gate and
shares a bounded service concurrency budget with planning. No production route,
RPC, worker, model call or actual publication is wired by this slice.

The internal release-flow coordinator only consumes a release intent that was
already durably recorded by a separately authenticated human release action.
It never creates that intent from description confirmation or from the
`confirmVersionPublish` policy flag. It then replays continuation and planning
under the current checkpoint before asking Cloud for the permit. This keeps
description approval, version-policy selection and release authorization as
three distinct facts; the coordinator remains an internal default-off adapter.

## Isolated Cloud commit admission

The internal commit client is a distinct irreversible step after the durable
human release intent and exact Cloud permit exist. It posts only
`schemaVersion`, `applicationId` and `permitId` to Cloud's isolated
`/api/skills/publication-descriptions/internal/commit` endpoint. The response
must bind the same application, original task and permit, and return one of the
known isolated queue states. A retry after an uncertain response uses the same
permit; Cloud returns the same durable admission instead of creating duplicate
publication work.

The separate
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_COMMIT_ENABLED` gate defaults
off and also requires the derivation, planning and release-authorization
configuration. This slice adds no RPC, HTTP route, model tool, worker or
automatic invocation. Cloud's own commit-admission switch remains independently
default-off. When later wired and both sides are enabled, admission can start
the asynchronous isolated publication worker; it is not merely a status read
or a reversible preview. It never enters the legacy original-candidate commit
path. An idempotent replay can also return a terminal `published` or
`publish_failed` state; callers must inspect the returned state, and a nil
error alone does not mean publication is still in progress. Production
activation and user-facing wiring are later steps.

The mandatory paired contract is run with
`node scripts/test-publication-description-commit-paired.mjs`. It pins the
reviewed Cloud commit, requires a real PostgreSQL DSN, and exercises the
actual Cloud permit/admission handler with this CatsCo client/coordinator.
The fixture is synthetic and does not publish a user Skill or enable any
production gate.

The strict `scripts/test-publication-description-plan-paired.mjs` runner pins
Cloud's reviewed planner revision and requires a real PostgreSQL DSN; missing
dependencies or any skipped case fail. It pairs actual Cloud intake, retention,
bounded workers, PostgreSQL planner/reservation and verified HTTPS with this
CatsCo client/coordinator. It covers reconstruction after a committed lost reply,
one exact target/version, unchanged original bytes, no commits/old plans, source
cancellation, publisher suspension and default-off rejection. CatsCo authority,
accepted report and Runtime source remain synthetic, not dual-database or
production/paid-model E2E evidence. The private Cloud repository hosts the pinned
CI job so public CatsCo PRs receive no private-repository credential.

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
