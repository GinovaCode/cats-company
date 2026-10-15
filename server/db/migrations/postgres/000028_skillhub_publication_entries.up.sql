CREATE TABLE IF NOT EXISTS skillhub_publication_entries (
    entry_id VARCHAR(41) PRIMARY KEY CHECK (entry_id ~ '^pubentry_[a-f0-9]{32}$'),
    request_id VARCHAR(128) NOT NULL CHECK (request_id ~ '^[A-Za-z0-9_-]{8,128}$'),
    actor_uid BIGINT NOT NULL CHECK (actor_uid > 0),
    bot_uid BIGINT NOT NULL CHECK (bot_uid > 0),
    entrypoint TEXT NOT NULL CHECK (entrypoint = 'workspace'),
    device_id VARCHAR(128) NOT NULL CHECK (device_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    body_id VARCHAR(128) NOT NULL CHECK (body_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    installation_id VARCHAR(128) NOT NULL CHECK (installation_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    route_generation BIGINT NOT NULL CHECK (route_generation > 0),
    workspace_generation VARCHAR(128) NOT NULL CHECK (workspace_generation ~ '^[A-Za-z0-9._:-]{1,128}$'),
    local_skill_id VARCHAR(128) NOT NULL CHECK (local_skill_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    state TEXT NOT NULL DEFAULT 'waiting_runtime' CHECK (state = 'waiting_runtime'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (actor_uid, request_id),
    CHECK (actor_uid <> bot_uid)
);
CREATE INDEX IF NOT EXISTS idx_skillhub_publication_actor_created ON skillhub_publication_entries(actor_uid, created_at);

CREATE TABLE IF NOT EXISTS skillhub_publication_outbox (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind = 'capture_candidate'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS skillhub_publication_admission (
    lock_id SMALLINT PRIMARY KEY CHECK (lock_id = 1)
);
INSERT INTO skillhub_publication_admission (lock_id) VALUES (1) ON CONFLICT DO NOTHING;
