DO $$
BEGIN
    IF to_regclass('skillhub_publication_upload_grants') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_upload_grants IN ACCESS EXCLUSIVE MODE;
        IF EXISTS (SELECT 1 FROM skillhub_publication_upload_grants) THEN
            RAISE EXCEPTION 'cannot discard retained publication upload authority';
        END IF;
        DROP TABLE skillhub_publication_upload_grants;
    END IF;
END;
$$;
