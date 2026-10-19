DO $$
BEGIN
    IF to_regclass('skillhub_publication_commit_deliveries') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_commit_deliveries IN ACCESS EXCLUSIVE MODE;
        IF EXISTS(SELECT 1 FROM skillhub_publication_commit_deliveries) THEN
            RAISE EXCEPTION 'retained publication commit delivery prevents rollback';
        END IF;
        DROP TABLE skillhub_publication_commit_deliveries;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_commit_delivery();
END;
$$;
