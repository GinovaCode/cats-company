DO $$
BEGIN
    ALTER TABLE skillhub_publication_entries DROP CONSTRAINT IF EXISTS skillhub_publication_entries_state_check;
    ALTER TABLE skillhub_publication_entries ADD CONSTRAINT skillhub_publication_entries_state_check CHECK (state IN ('waiting_runtime','cancelled'));
END;
$$;
