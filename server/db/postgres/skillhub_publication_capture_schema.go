package postgres

// Mirror migration 000042. No evidence is inferred from old probes or grants.
const createSkillHubPublicationCapturesTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_captures (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    candidate_revision INTEGER NOT NULL CHECK (candidate_revision=1),
    request_hash VARCHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    source_snapshot_hash VARCHAR(64) NOT NULL CHECK (source_snapshot_hash ~ '^[a-f0-9]{64}$'),
    request_bytes BIGINT NOT NULL CHECK (request_bytes BETWEEN 1 AND 33554432),
    route_generation BIGINT NOT NULL CHECK (route_generation>0),
    node_id VARCHAR(128) NOT NULL CHECK (node_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    connection_id VARCHAR(128) NOT NULL CHECK (connection_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    captured_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_capture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'retained publication capture cannot be changed or deleted';
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_capture ON skillhub_publication_captures;
CREATE TRIGGER guard_skillhub_publication_capture BEFORE UPDATE OR DELETE ON skillhub_publication_captures
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_capture();
`
