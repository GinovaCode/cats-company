package postgres

// Mirror migration 000039. Scheduling and remote evidence, not action authority.
const createSkillHubPublicationCancellationDeliveriesTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_cancel_deliveries (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_cancel_intents(entry_id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('pending','leased','confirmed','needs_attention','exhausted')),
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 8),
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_code TEXT NOT NULL DEFAULT '' CHECK (last_code IN ('','retry','conflict','not_found')),
    remote_cancelled_at TIMESTAMPTZ,
    remote_observed_at TIMESTAMPTZ,
    CHECK ((state='leased' AND lease_token IS NOT NULL AND lease_token ~ '^[a-f0-9]{32}$' AND lease_until IS NOT NULL) OR
           (state<>'leased' AND lease_token IS NULL AND lease_until IS NULL)),
    CHECK ((state='confirmed' AND remote_cancelled_at IS NOT NULL AND remote_observed_at IS NOT NULL
            AND remote_cancelled_at<=remote_observed_at AND last_code='') OR
           (state<>'confirmed' AND remote_cancelled_at IS NULL AND remote_observed_at IS NULL)),
    CHECK (state<>'exhausted' OR attempts=8),
    CHECK (state<>'pending' OR attempts<8)
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_cancel_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'retained publication cancellation delivery cannot be deleted';
    END IF;
    IF TG_OP='UPDATE' AND (NEW.entry_id IS DISTINCT FROM OLD.entry_id OR NEW.attempts<OLD.attempts OR
        (OLD.state IN ('confirmed','needs_attention','exhausted') AND NEW IS DISTINCT FROM OLD)) THEN
        RAISE EXCEPTION 'retained publication cancellation delivery cannot be reset';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents c
        JOIN skillhub_publication_entries e USING(entry_id)
        JOIN skillhub_publication_cloud_receipts r USING(entry_id)
        WHERE c.entry_id=NEW.entry_id AND c.requires_cloud AND e.state='cancelled') THEN
        RAISE EXCEPTION 'publication cancellation delivery requires retained intent and receipt';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_cancel_delivery ON skillhub_publication_cancel_deliveries;
CREATE TRIGGER guard_skillhub_publication_cancel_delivery BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_cancel_deliveries
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_cancel_delivery();
`
