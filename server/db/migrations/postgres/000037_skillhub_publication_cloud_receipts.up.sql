CREATE TABLE IF NOT EXISTS skillhub_publication_cloud_receipts (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    issuer VARCHAR(256) NOT NULL CHECK (length(issuer) > 0),
    audience VARCHAR(256) NOT NULL CHECK (length(audience) > 0 AND audience <> issuer),
    action VARCHAR(32) NOT NULL CHECK (action = 'candidate.upload'),
    candidate_revision INTEGER NOT NULL CHECK (candidate_revision = 1),
    acceptance_epoch INTEGER NOT NULL CHECK (acceptance_epoch BETWEEN 1 AND 8),
    request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    source_snapshot_hash CHAR(64) NOT NULL CHECK (source_snapshot_hash ~ '^[a-f0-9]{64}$'),
    request_bytes BIGINT NOT NULL CHECK (request_bytes BETWEEN 1 AND 78643200),
    delegation_id VARCHAR(41) NOT NULL UNIQUE CHECK (delegation_id ~ '^pubgrant_[a-f0-9]{32}$'),
    idempotency_key VARCHAR(128) NOT NULL UNIQUE CHECK (idempotency_key ~ '^pubcandidate_[a-f0-9]{32}$'),
    cloud_task_id VARCHAR(44) NOT NULL CHECK (cloud_task_id ~ '^pubtask_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$'),
    event_id VARCHAR(45) NOT NULL CHECK (event_id ~ '^pubevent_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (audience, cloud_task_id),
    UNIQUE (audience, event_id)
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_cloud_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'retained publication Cloud receipt is immutable';
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_cloud_receipt ON skillhub_publication_cloud_receipts;
CREATE TRIGGER guard_skillhub_publication_cloud_receipt BEFORE UPDATE OR DELETE ON skillhub_publication_cloud_receipts
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_cloud_receipt();
