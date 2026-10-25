DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_acceptances') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_acceptances) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication description acceptances';
        END IF;
    END IF;
    DROP TABLE IF EXISTS skillhub_publication_description_acceptances;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description_acceptance();
END;
$$;
