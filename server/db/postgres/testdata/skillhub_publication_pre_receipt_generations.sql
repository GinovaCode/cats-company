-- Frozen connection ledger from cats-company 9b06eecf (pre-receipt receiver).
-- Complements pre620 users/Bot fixtures for issuing a historical upload grant.
CREATE TABLE IF NOT EXISTS bot_connection_generations (
    bot_uid BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
