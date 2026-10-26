DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_release_permits') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_release_permits) THEN
            RAISE EXCEPTION 'cannot remove retained publication description release permits';
        END IF;
        DROP TABLE IF EXISTS skillhub_publication_description_release_permits;
    END IF;
    DROP TRIGGER IF EXISTS guard_skillhub_publication_no_legacy_commit_after_description_release ON skillhub_publication_commit_intents;
    DROP TRIGGER IF EXISTS guard_skillhub_publication_release_started_entry_cancel ON skillhub_publication_entries;
    DROP TRIGGER IF EXISTS guard_skillhub_publication_release_started_cancel_intent ON skillhub_publication_cancel_intents;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_no_legacy_commit_after_description_release();
    DROP FUNCTION IF EXISTS guard_skillhub_publication_release_started_cancellation();
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description_release_permit();
END;
$$;
