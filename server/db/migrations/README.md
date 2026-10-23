# CatsCompany PostgreSQL Migrations

This directory is the public, versioned home for database schema migrations.

Current state:

- `postgres/000001_baseline` marks the production PostgreSQL schema that is still created by `server/db/postgres/schema.go`.
- `CreateSchema()` is the schema module: it creates and safely extends ordinary tables, indexes, constraints, and triggers when the service starts.
- Every schema change, including additive tables, columns, indexes, constraints and triggers, must also have a uniquely numbered `up` / `down` SQL migration mirroring the same DDL. `CreateSchema()` remains the runtime authority; keep both tracks consistent and test their equivalence.
- Data backfills/transformations belong only in the numbered migration, not startup DDL. Apply the ordering and release gates in [docs/DB_MIGRATIONS.md](../../../docs/DB_MIGRATIONS.md): test empty creation and a named historical schema upgrade, repeat the upgrade to prove idempotence, and run database tests with a real test DSN in CI.
- Do not edit an already-applied migration. Add a new one.
- `000030_skillhub_publication_delivery` adds inert publication delivery leases to the existing outbox. Its down migration preserves pre-existing intents but refuses to discard any delivery attempt/receipt evidence; see [publication persistence](../../../docs/skillhub-publication-entries.md).
- `000035_skillhub_publication_cancellation` expands the entry-state constraint without deleting intents or outbox evidence. Down refuses retained cancellations; all delivery workers must be cancellation-aware before the feature flag is enabled. See the cancellation section in the same publication document.
- `000036_skillhub_publication_upload_grants` adds internal upload-grant storage, not a reachable upload API. Down refuses any retained authority/replay evidence; roll it back before its parent publication tables. See the upload-grant foundation section in the same publication document.
- `000053_skillhub_publication_description_application` retains immutable confirmed-description application identities and one-shot result metadata; it registers no route or executor. Down refuses retained application evidence and must precede rollback of 000052. See [description application](../../../docs/skillhub-publication-description-application.md).
- MySQL is not part of the migration system. Keep MySQL compatibility fixes in code only unless the product explicitly reintroduces MySQL migrations.

Sensitive values never belong here:

- real database URLs or passwords
- `.pgpass`, `.my.cnf`, private keys, or service tokens
- production backup dumps
- command output that contains a full DSN

Use `scripts/db-migrate.sh` with a server-local environment file for real runs.
