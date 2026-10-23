# Confirmed description application

This step retains an application identity and the metadata of one independent
candidate derived from an exactly confirmed description. It does not publish a
Skill, overwrite its source, resume a model or complete the automatic repair loop.

## Default-off internal boundary

`applyPublicationDescription` is an internal coordinator. No HTTP route, RPC,
model tool, worker or production executor is registered for it. This migration
enables no production flag. Existing publication/admission switches remain
unchanged; enabling those switches does not register this coordinator.

The three store operations are prepare, current-authority get and complete.
They require the original active human publisher, current enabled Bot with the
same owner/body, an unsettled workspace entry, and the private same-Bot repair
task's current owner/type/topic/exact two-member membership. Cancel or commit
intents block all three, including exact result replay. Retained rows remain for
audit after settlement, but these methods do not expose a post-settlement history
API. Transfer does not grant a later owner access to the original application.

## Identity and durable state

Migration `000053_skillhub_publication_description_application` and the mirrored
startup DDL retain one row per entry/round, linked to migration 000052's immutable
confirmation. Preparation requires an exactly confirmed draft for the current
round and the original retained Cloud receipt. The application input binds that
receipt, the full draft (task/round/evidence/selector/exact text) and confirmation
key. `identity_hash` is the domain-separated Go-JSON input digest, and the
immutable idempotency key is `pubdesc_` followed by that digest.

Go validates the computed identity on reads and writes. SQL guards additionally
enforce exact stored draft/receipt/confirmation/round binding, ID/hash shape and
correlation, current authority, and a single completion. Neither a digest nor
an application ID or browser assertion is authority. JSON receipt fields retain
their Go names, including `receipt.Query.RequestHash` (capitalized).

Prepare commits before external work. Duplicate prepares serialize on the
entry lock and return the same row. Completion accepts one result and timestamp;
an exact adapter replay recovers it, while different metadata conflicts. Direct
SQL UPDATE/DELETE cannot replace a completed row. Nonempty down migration
refuses to erase application evidence; empty down/up is idempotent and must be
ordered before rollback of 000052.

`Get` deliberately uses the same entry/task authority locks as prepare/complete:
it is a short current-authority checkpoint between external operations, not a
read-only historical status query. These locks serialize with cancellation and
membership changes; no database lock spans Runtime/Cloud network calls. A future
history API must be separate and must not serve as an execution checkpoint.

## Required adapter behavior

The CatsCo trusted Cloud client is implemented in
`server/skillhub_publication_description_derivation_client.go`. It is a
contract-only adapter and is not registered as a production executor. The
environment switch is `CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED`;
when it is false or absent, calls fail closed without network I/O. Enabling the
client requires a pinned HTTPS endpoint/audience, a dedicated
`pubderive_` bearer secret and a bounded timeout. The client sends only the
application/task/hash/proposal bindings, never cookies, Origin or source bytes,
and rejects duplicate or unknown response JSON fields. It accepts a response
only when the application, task, original hashes, proposal digest, derivation
algorithm/schema and changed derived hashes all match the retained application.

This adapter currently returns Cloud's durable derivation receipt metadata to
the coordinator. It does not expose the retained private bytes or wire a
publication continuation; a separately authenticated Cloud read/recovery
contract is still required before a later step can consume the derived copy.

The coordinator executes prepare, `VerifySource`, current-authority get,
`DeriveAndRetain`, `VerifySource` again, and complete. Context cancellation is
checked before dispatch and before completion. A previously accepted result is
returned without repeating derivation; any subsequent continuation needs its
own fresh source/authority check.

- `VerifySource` must read the live source using the original trusted capture
  under its Runtime workspace fence. Replaying frozen upload bytes alone does
  not prove that the current source is still the same.
- `DeriveAndRetain` must consume the exact retained confirmation, rederive that
  text deterministically, prove every other byte unchanged, and durably retain
  one independent private copy under `app.ID`, with quota/retention accounting.
  It must recover that same copy after a lost reply; it must not overwrite the
  source or substitute another candidate under the same identity.
- The result includes byte count and Cloud derivation receipt. Supported
  missing-description transformations must change request and candidate hashes;
  a no-op result is rejected. The result binds the original request/candidate,
  derived request/candidate, algorithm/schema, validation/proposal digests and
  limits fingerprint. Cloud canonical-JSON digests are distinct from CatsCo's
  Go-JSON confirmation/evidence digests.

Stored result metadata checks correlation, not the existence or authenticity of
retained candidate bytes. An HTTP client/model hash or validation-only response
cannot implement `DeriveAndRetain`. If retention succeeds but the reply or database
completion is lost, retry only the same deterministic application identity;
recheck current source/authority and recover the same copy. Cancellation in this
gap blocks completion; private-copy lifecycle accounting remains the retention
adapter's responsibility. Never bypass cancellation to recover an execution.

## Remaining scope and verification

This step never clears `waiting_confirmation`, changes an immutable repair-round
result, resets/spends the three-round budget, or retries an uncertain paid model
call. Production transports, durable private-copy retention, model-call
idempotency/reconciliation, controlled continuation and the two publication
entry points still need separate implementation. Chinese presentation text and
author-managed images remain later work.

Real PostgreSQL tests exercise prepare/get/complete, restart/lost-reply and
concurrent replay, exact-result conflicts, SQL forged inputs/results and immutable
rows, authority changes, cancellation races and down/up lifecycle. Derivation
metadata is synthetic: these are database contracts, not real Cloud retention or
model/file repair E2E. Tests without `CATS_PG_TEST_DSN` skip locally; CI's required
publication job runs all `TestPostgresSkillHubPublication*` tests against real
PostgreSQL, including the concurrency/recovery subset under the race detector.
