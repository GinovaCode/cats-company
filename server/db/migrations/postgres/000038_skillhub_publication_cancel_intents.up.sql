CREATE TABLE IF NOT EXISTS skillhub_publication_cancel_intents (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    cancel_id VARCHAR(42) NOT NULL UNIQUE CHECK (cancel_id ~ '^pubcancel_[a-f0-9]{32}$'),
    requires_cloud BOOLEAN NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_cancel_intent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    local_state TEXT;
    issued BOOLEAN;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'retained publication cancellation intent is immutable';
    END IF;
    SELECT state INTO local_state FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    SELECT EXISTS(SELECT 1 FROM skillhub_publication_upload_grants WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_cloud_receipts WHERE entry_id=NEW.entry_id) INTO issued;
    IF local_state IS DISTINCT FROM 'cancelled' OR NEW.requires_cloud IS DISTINCT FROM issued THEN
        RAISE EXCEPTION 'publication cancellation intent does not match local authority evidence';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_cancel_intent ON skillhub_publication_cancel_intents;
CREATE TRIGGER guard_skillhub_publication_cancel_intent BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_cancel_intents
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_cancel_intent();
CREATE OR REPLACE FUNCTION guard_skillhub_publication_cancelled_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state IS DISTINCT FROM OLD.state AND
        EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=OLD.entry_id) THEN
        RAISE EXCEPTION 'retained publication cancellation intent prevents local resurrection';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_cancelled_entry ON skillhub_publication_entries;
CREATE TRIGGER guard_skillhub_publication_cancelled_entry BEFORE UPDATE OF state ON skillhub_publication_entries
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_cancelled_entry();
