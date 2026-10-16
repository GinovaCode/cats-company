DO $$
BEGIN
    LOCK TABLE skillhub_publication_entries IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM skillhub_publication_entries WHERE state <> 'waiting_runtime') THEN
        RAISE EXCEPTION 'cannot roll back with retained cancellation evidence';
    END IF;
    ALTER TABLE skillhub_publication_entries DROP CONSTRAINT IF EXISTS skillhub_publication_entries_state_check;
    ALTER TABLE skillhub_publication_entries ADD CONSTRAINT skillhub_publication_entries_state_check CHECK (state IN ('waiting_runtime'));
END;
$$;
