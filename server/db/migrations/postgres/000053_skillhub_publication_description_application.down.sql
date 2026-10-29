DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_applications') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_applications) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication description applications';
        END IF;
    END IF;
    DROP TABLE IF EXISTS skillhub_publication_description_applications;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description_application();
END;
$$;
