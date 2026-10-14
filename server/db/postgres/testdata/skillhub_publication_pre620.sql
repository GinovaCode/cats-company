-- Frozen relevant-table baseline: cats-company bf2e938d (pre-PR620).
-- users and bot_config definitions from server/db/postgres/schema.go;
-- synthetic rows only. Not a full production schema/dump.
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    email VARCHAR(255) DEFAULT NULL,
    phone VARCHAR(32) DEFAULT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    avatar_url VARCHAR(512) DEFAULT NULL,
    account_type VARCHAR(16) NOT NULL DEFAULT 'human' CHECK (account_type IN ('human','bot','service')),
    pass_hash BYTEA NOT NULL,
    state SMALLINT NOT NULL DEFAULT 0,
    bot_disclose BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS bot_config (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    owner_id BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    api_endpoint VARCHAR(512) DEFAULT '',
    model VARCHAR(128) DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    config JSONB DEFAULT NULL,
    api_key VARCHAR(128) DEFAULT NULL,
    visibility VARCHAR(16) NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','private')),
    skills_visibility VARCHAR(16) NOT NULL DEFAULT 'owner' CHECK (skills_visibility IN ('owner','authorized','public')),
    tenant_name VARCHAR(128) DEFAULT NULL,
    body_id VARCHAR(128) DEFAULT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'general',
    description TEXT NOT NULL DEFAULT '',
    artifact_upload_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    skill_mutation_mode VARCHAR(16) NOT NULL DEFAULT 'owner_only'
        CONSTRAINT bot_config_skill_mutation_mode_check CHECK (skill_mutation_mode IN ('owner_only','shared_live')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO users (id, username, display_name, account_type, pass_hash)
VALUES (1001, 'legacy-publisher', 'Legacy Publisher', 'human', decode('00','hex')),
       (1002, 'legacy-bot', 'Legacy Bot', 'bot', decode('00','hex'));
INSERT INTO bot_config (user_id, owner_id, body_id, model, config)
VALUES (1002, 1001, 'legacy-body', 'unchanged-model', '{"legacy":true}');
