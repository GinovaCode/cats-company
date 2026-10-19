CREATE TABLE IF NOT EXISTS skillhub_publication_commit_deliveries (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_commit_intents(entry_id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('pending','leased','accepted','needs_attention','exhausted')),
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 8),
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_code TEXT NOT NULL DEFAULT '' CHECK (last_code IN ('','retry','cancelled','denied','conflict','not_found')),
    permit_id TEXT,
    CHECK ((state='leased' AND lease_token ~ '^[a-f0-9]{32}$' AND lease_token IS NOT NULL AND lease_until IS NOT NULL) OR
           (state<>'leased' AND lease_token IS NULL AND lease_until IS NULL)),
    CHECK ((state='accepted' AND permit_id ~ '^pubpermit_[a-f0-9]{32}$' AND permit_id IS NOT NULL AND last_code='') OR
           (state<>'accepted' AND permit_id IS NULL)),
    CHECK (state<>'exhausted' OR attempts=8),
    CHECK (state<>'pending' OR attempts<8)
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_commit_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'retained publication commit delivery cannot be deleted';
    END IF;
    IF TG_OP='UPDATE' AND (NEW.entry_id IS DISTINCT FROM OLD.entry_id OR NEW.attempts<OLD.attempts OR
        (OLD.state IN ('accepted','needs_attention','exhausted') AND NEW IS DISTINCT FROM OLD)) THEN
        RAISE EXCEPTION 'retained publication commit delivery cannot be reset';
    END IF;
    IF NEW.state='accepted' AND NOT EXISTS(SELECT 1 FROM skillhub_publication_commit_permits p
        WHERE p.entry_id=NEW.entry_id AND p.permit_id=NEW.permit_id) THEN
        RAISE EXCEPTION 'commit acknowledgement requires matching local permit';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_commit_delivery ON skillhub_publication_commit_deliveries;
CREATE TRIGGER guard_skillhub_publication_commit_delivery BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_commit_deliveries
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_commit_delivery();
