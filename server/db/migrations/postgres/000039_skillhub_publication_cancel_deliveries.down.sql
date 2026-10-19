DO $$
BEGIN
    IF to_regclass('skillhub_publication_cancel_deliveries') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_cancel_deliveries IN ACCESS EXCLUSIVE MODE;
        IF EXISTS(SELECT 1 FROM skillhub_publication_cancel_deliveries) THEN
            RAISE EXCEPTION 'cannot remove retained publication cancellation deliveries';
        END IF;
        DROP TABLE skillhub_publication_cancel_deliveries;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_cancel_delivery();
END;
$$;
