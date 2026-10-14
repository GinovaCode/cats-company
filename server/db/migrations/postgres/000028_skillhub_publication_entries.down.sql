-- Rollback is only safe before any publication intent has been retained.
-- Do not silently delete private intent/outbox/replay evidence. Stop old/new
-- writers and back up before an operator runs this migration.
DO $$
DECLARE
    target TEXT;
    has_rows BOOLEAN;
BEGIN
    PERFORM set_config('lock_timeout', '3s', true);
    -- Match admission-first writer ordering; retain locks through the drops.
    FOREACH target IN ARRAY ARRAY['skillhub_publication_admission', 'skillhub_publication_entries', 'skillhub_publication_outbox'] LOOP
        IF to_regclass(target) IS NOT NULL THEN
            EXECUTE format('LOCK TABLE %I IN ACCESS EXCLUSIVE MODE', target);
        END IF;
    END LOOP;
    FOREACH target IN ARRAY ARRAY['skillhub_publication_entries', 'skillhub_publication_outbox'] LOOP
        IF to_regclass(target) IS NOT NULL THEN
            EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I)', target) INTO has_rows;
            IF has_rows THEN
                RAISE EXCEPTION 'publication entries rollback refused: retained publication evidence exists';
            END IF;
        END IF;
    END LOOP;
    DROP TABLE IF EXISTS skillhub_publication_outbox;
    DROP TABLE IF EXISTS skillhub_publication_entries;
    DROP TABLE IF EXISTS skillhub_publication_admission;
END;
$$;
