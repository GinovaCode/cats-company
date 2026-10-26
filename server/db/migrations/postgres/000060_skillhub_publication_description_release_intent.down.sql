DO $$ BEGIN
    IF to_regclass('skillhub_publication_description_release_intents') IS NOT NULL
        AND EXISTS(SELECT 1 FROM skillhub_publication_description_release_intents) THEN
        RAISE EXCEPTION 'cannot remove retained publication description release intents';
    END IF;
END $$;
DROP TABLE IF EXISTS skillhub_publication_description_release_intents;
DROP FUNCTION IF EXISTS guard_skillhub_publication_description_release_intent();
