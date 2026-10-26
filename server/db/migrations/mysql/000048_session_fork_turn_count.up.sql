-- 000048_session_fork_turn_count (up, MySQL)
ALTER TABLE session_forks ADD COLUMN source_turn_count BIGINT NOT NULL DEFAULT 0;
