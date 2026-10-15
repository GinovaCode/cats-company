-- Stop consumers and back up first. Never erase delivery/fencing evidence.
DO $$
BEGIN
    PERFORM set_config('lock_timeout', '3s', true);
    LOCK TABLE skillhub_publication_outbox IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM skillhub_publication_outbox WHERE delivery_attempts > 0) THEN
        RAISE EXCEPTION 'publication delivery rollback refused: retained delivery evidence exists';
    END IF;
    DROP INDEX IF EXISTS idx_skillhub_publication_delivery_due;
    DROP INDEX IF EXISTS idx_skillhub_publication_bot_created;
    ALTER TABLE skillhub_publication_outbox
        DROP CONSTRAINT IF EXISTS skillhub_publication_delivery_consistency,
        DROP COLUMN IF EXISTS delivery_state,
        DROP COLUMN IF EXISTS delivery_attempts,
        DROP COLUMN IF EXISTS next_attempt_at,
        DROP COLUMN IF EXISTS lease_token,
        DROP COLUMN IF EXISTS lease_node,
        DROP COLUMN IF EXISTS lease_connection,
        DROP COLUMN IF EXISTS lease_generation,
        DROP COLUMN IF EXISTS lease_until,
        DROP COLUMN IF EXISTS last_code,
        DROP COLUMN IF EXISTS source_verified_at;
END;
$$;
