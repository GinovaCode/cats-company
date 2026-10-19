DO $$
BEGIN
    IF to_regclass('skillhub_publication_entries') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_entries IN ACCESS EXCLUSIVE MODE;
    END IF;
    IF to_regclass('skillhub_publication_cancel_intents') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_cancel_intents IN ACCESS EXCLUSIVE MODE;
        IF EXISTS (SELECT 1 FROM skillhub_publication_cancel_intents) THEN
            RAISE EXCEPTION 'cannot remove retained publication cancellation intents';
        END IF;
        DROP TABLE skillhub_publication_cancel_intents;
    END IF;
    IF to_regclass('skillhub_publication_entries') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS guard_skillhub_publication_cancelled_entry ON skillhub_publication_entries;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_cancelled_entry();
    DROP FUNCTION IF EXISTS guard_skillhub_publication_cancel_intent();
END;
$$;
