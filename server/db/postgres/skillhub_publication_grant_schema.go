package postgres

// Keep mirrored in migration 000036. One bounded, retained grant per entry.
const createSkillHubPublicationUploadGrantsTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_upload_grants (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id),
    grant_id VARCHAR(41) NOT NULL UNIQUE CHECK (grant_id ~ '^pubgrant_[a-f0-9]{32}$'),
    issuer VARCHAR(256) NOT NULL CHECK (length(issuer) > 0),
    audience VARCHAR(256) NOT NULL CHECK (length(audience) > 0 AND audience <> issuer),
    action VARCHAR(32) NOT NULL CHECK (action = 'candidate.upload'),
    candidate_revision INTEGER NOT NULL CHECK (candidate_revision = 1),
    request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    source_snapshot_hash CHAR(64) NOT NULL CHECK (source_snapshot_hash ~ '^[a-f0-9]{64}$'),
    request_bytes BIGINT NOT NULL CHECK (request_bytes BETWEEN 1 AND 78643200),
    idempotency_key VARCHAR(128) NOT NULL UNIQUE CHECK (idempotency_key ~ '^pubcandidate_[a-f0-9]{32}$'),
    token_hash CHAR(64) NOT NULL CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    authority_epoch INTEGER NOT NULL CHECK (authority_epoch BETWEEN 1 AND 8),
    route_generation BIGINT NOT NULL CHECK (route_generation > 0),
    node_id VARCHAR(128) NOT NULL,
    connection_id VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > issued_at AND expires_at <= issued_at + INTERVAL '120 seconds')
);
`
