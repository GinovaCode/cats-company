DO $$
BEGIN
    IF to_regclass('skillhub_publication_repair_tasks') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM skillhub_publication_repair_tasks) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication repair budget';
        END IF;
        DROP TABLE skillhub_publication_repair_rounds;
        DROP TABLE skillhub_publication_repair_tasks;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_repair();
END;
$$;
