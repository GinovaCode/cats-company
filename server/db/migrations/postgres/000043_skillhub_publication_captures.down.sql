DO $$
BEGIN
    IF to_regclass('skillhub_publication_captures') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_captures IN ACCESS EXCLUSIVE MODE;
        IF EXISTS(SELECT 1 FROM skillhub_publication_captures) THEN
            RAISE EXCEPTION 'retained publication captures prevent rollback';
        END IF;
        DROP TABLE skillhub_publication_captures;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_capture();
END;
$$;
