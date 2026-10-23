DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_confirmations') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_confirmations) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication description confirmations';
        END IF;
    END IF;
    IF to_regclass('skillhub_publication_description_drafts') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_drafts) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication description drafts';
        END IF;
    END IF;
    DROP TABLE IF EXISTS skillhub_publication_description_confirmations;
    DROP TABLE IF EXISTS skillhub_publication_description_drafts;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description();
END;
$$;
