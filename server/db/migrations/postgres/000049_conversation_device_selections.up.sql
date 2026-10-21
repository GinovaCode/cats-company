CREATE TABLE IF NOT EXISTS conversation_device_selections (
    actor_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_key VARCHAR(768) COLLATE "C" NOT NULL,
    device_id VARCHAR(128) NOT NULL,
    revision VARCHAR(64) COLLATE "C" NOT NULL,
    source VARCHAR(64) NOT NULL,
    updated_at_ms BIGINT NOT NULL,
    PRIMARY KEY (actor_uid, session_key)
);
