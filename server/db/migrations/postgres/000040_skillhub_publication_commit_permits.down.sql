DO $$
BEGIN
    IF to_regclass('skillhub_publication_commit_permits') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_commit_permits IN ACCESS EXCLUSIVE MODE;
        IF EXISTS(SELECT 1 FROM skillhub_publication_commit_permits) THEN
            RAISE EXCEPTION 'cannot remove retained publication commit permits';
        END IF;
    END IF;
    IF to_regclass('skillhub_publication_commit_intents') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_commit_intents IN ACCESS EXCLUSIVE MODE;
        IF EXISTS(SELECT 1 FROM skillhub_publication_commit_intents) THEN
            RAISE EXCEPTION 'cannot remove retained publication commit intents';
        END IF;
    END IF;
    DROP TABLE IF EXISTS skillhub_publication_commit_permits;
    DROP TABLE IF EXISTS skillhub_publication_commit_intents;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_commit_authority();
END;
$$;
