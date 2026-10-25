# Retaining a confirmed description's validation report

After an exact human description confirmation has been deterministically applied
to a retained private candidate, the internal `acceptConfirmedPublicationDescription`
coordinator records the trusted peer's complete passing report. This checkpoint
is durable evidence for a later controlled continuation. It does not start one.

## Binding and recovery

Migration `000055` and the startup schema create an immutable acceptance per
application. The application identity already binds the original Cloud receipt,
repair task, round, exact draft and confirmation key. The acceptance additionally
binds the full retained derivation result and complete report, including report
UUID, candidate hash, rules and limits. It uses a separate domain-separated
Go-JSON identity; this is not Cloud's canonical-JSON digest.

The report types and semantic validator live in `server/store`; server aliases
preserve existing JSON tags and report/evidence digest bytes. Existing strict
wire decoders still reject missing, duplicate, null and unknown fields.

First acceptance checks current application authority, original runtime source,
and the trusted derived report. The database transaction then rechecks the exact
application, active human, current Bot owner/body, private owner-plus-Bot task,
latest confirmed round, and absence of cancellation/commit. The report must be
complete, untruncated, passing and bound to the derived candidate and limits.
Authority locks are never held across network calls.

Retries recover the first retained report without asking Cloud to generate a new
report UUID. Exact writes replay; different reports conflict without replacing
history. Concurrent coordinators recover the winner after another current
authority and original-source check. A lost database reply is recovered by the
next invocation, not by repeating a paid operation. Reads enforce the same
current authority as writes, including after a process restart.

Remote source checks are point-in-time observations, not atomic with the
PostgreSQL commit. The coordinator rechecks after persistence before returning.
If the source or authority changes, retained evidence may remain historical but
cannot be returned as a current continuation checkpoint. A future consumer must
perform its own current-authority and source checks.

## Scope

The completed repair round stays immutable at `waiting_confirmation`. Acceptance
neither spends nor resets the three-round budget. It does not allocate a lease,
modify the original Skill, publish a package or create a commit intent. It does
not clear a waiting state or expose an effective validated status to the UI.

There is no production route, RPC, model tool, worker, executor registration or
new feature switch. The store interface and coordinator are internal trusted
components. Browser/model-supplied reports are never accepted through a handler.

The remaining M3 work includes consuming this checkpoint in controlled
continuation, private candidate ancestry and later-round derivation, uncertain
paid-call reconciliation, and both publishing entry points. Chinese presentation
content and author images remain later work.

## Verification boundaries

Domain and coordinator tests cover exact binding, report identity, lost replies,
concurrent-winner recovery, pointer isolation, current source/authority changes
and cancellation. PostgreSQL tests cover restart/startup recovery, immutable SQL
guards, conflicting reports, current authority, cancellation races and concurrent
adapters. Their candidate metadata and trusted peer reports are synthetic.

Required Linux CI runs the real PostgreSQL contracts and race subsets. A local
database skip is not a PostgreSQL pass. These tests are not a production or paid
model repair E2E, nor a combined Cloud/Runtime/PostgreSQL E2E.
