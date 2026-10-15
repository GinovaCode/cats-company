ALTER TABLE skillhub_publication_outbox
    ADD COLUMN IF NOT EXISTS delivery_state TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending','leased','source_verified','needs_user_action')),
    ADD COLUMN IF NOT EXISTS delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK (delivery_attempts BETWEEN 0 AND 8),
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN IF NOT EXISTS lease_token VARCHAR(32) CHECK (lease_token ~ '^[a-f0-9]{32}$'),
    ADD COLUMN IF NOT EXISTS lease_node VARCHAR(128) CHECK (lease_node ~ '^[A-Za-z0-9._:-]{1,128}$'),
    ADD COLUMN IF NOT EXISTS lease_connection VARCHAR(128) CHECK (lease_connection ~ '^[A-Za-z0-9._:-]{1,128}$'),
    ADD COLUMN IF NOT EXISTS lease_generation BIGINT CHECK (lease_generation > 0),
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_code TEXT NOT NULL DEFAULT '' CHECK (last_code IN ('','publication.runtime_timeout','publication.runtime_unavailable','publication.source_missing','publication.source_changed','publication.source_unsafe','publication.runtime_unsupported','publication.delivery_exhausted')),
    ADD COLUMN IF NOT EXISTS source_verified_at TIMESTAMPTZ;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='skillhub_publication_delivery_consistency'
        AND conrelid='skillhub_publication_outbox'::regclass) THEN
        ALTER TABLE skillhub_publication_outbox ADD CONSTRAINT skillhub_publication_delivery_consistency CHECK (
            ((delivery_attempts=0 AND lease_token IS NULL AND lease_node IS NULL AND lease_connection IS NULL AND lease_generation IS NULL AND lease_until IS NULL AND delivery_state='pending')
             OR (delivery_attempts>0 AND lease_token IS NOT NULL AND lease_node IS NOT NULL AND lease_connection IS NOT NULL AND lease_generation IS NOT NULL AND lease_until IS NOT NULL))
            AND ((delivery_state='source_verified' AND source_verified_at IS NOT NULL AND last_code='') OR (delivery_state<>'source_verified' AND source_verified_at IS NULL))
        );
    END IF;
END;
$$;
CREATE INDEX IF NOT EXISTS idx_skillhub_publication_delivery_due ON skillhub_publication_outbox(delivery_state, next_attempt_at, entry_id);
CREATE INDEX IF NOT EXISTS idx_skillhub_publication_bot_created ON skillhub_publication_entries(bot_uid, created_at, entry_id);
