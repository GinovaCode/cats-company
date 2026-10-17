DO $$
BEGIN
    IF to_regclass('skillhub_publication_cloud_receipts') IS NOT NULL THEN
        LOCK TABLE skillhub_publication_cloud_receipts IN ACCESS EXCLUSIVE MODE;
        IF EXISTS (SELECT 1 FROM skillhub_publication_cloud_receipts) THEN
            RAISE EXCEPTION 'cannot remove retained publication Cloud receipts';
        END IF;
        DROP TABLE skillhub_publication_cloud_receipts;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_cloud_receipt();
END;
$$;
