package mysql

import (
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
)

var sessionForkSchema = []string{
	`ALTER TABLE topics ADD COLUMN history_revision BIGINT NOT NULL DEFAULT 0;`,
	`ALTER TABLE topics ADD COLUMN fork_state VARCHAR(16) NOT NULL DEFAULT '';`,
	`CREATE TABLE IF NOT EXISTS session_forks (
    actor_user_id BIGINT NOT NULL,
    request_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    source_topic_id VARCHAR(64) NOT NULL,
    target_topic_id VARCHAR(64) NOT NULL UNIQUE,
    agent_uid BIGINT NOT NULL,
    body_id VARCHAR(128) NOT NULL,
    request_name VARCHAR(80) NOT NULL DEFAULT '',
    source_revision BIGINT NOT NULL,
    target_revision BIGINT NOT NULL,
    source_last_seq BIGINT NOT NULL,
    message_id_map JSON NOT NULL,
    state VARCHAR(16) NOT NULL,
    error TEXT NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (actor_user_id, request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,
	`CREATE TRIGGER session_fork_message_insert BEFORE INSERT ON messages FOR EACH ROW
BEGIN
    DECLARE current_state VARCHAR(16);
    UPDATE topics SET history_revision = history_revision + 1 WHERE id = NEW.topic_id;
    SELECT fork_state INTO current_state FROM topics WHERE id = NEW.topic_id;
    IF current_state IN ('pending','failed') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'session_fork_not_ready';
    END IF;
END`,
	`CREATE TRIGGER session_fork_message_update BEFORE UPDATE ON messages FOR EACH ROW
BEGIN
    DECLARE current_state VARCHAR(16);
    IF NEW.topic_id <> OLD.topic_id THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'message_topic_is_immutable';
    END IF;
    UPDATE topics SET history_revision = history_revision + 1 WHERE id = NEW.topic_id;
    SELECT fork_state INTO current_state FROM topics WHERE id = NEW.topic_id;
    IF current_state IN ('pending','failed') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'session_fork_not_ready';
    END IF;
END`,
	`CREATE TRIGGER session_fork_message_delete BEFORE DELETE ON messages FOR EACH ROW
BEGIN
    DECLARE current_state VARCHAR(16);
    UPDATE topics SET history_revision = history_revision + 1 WHERE id = OLD.topic_id;
END`,
}

func (a *Adapter) ensureSessionForkSchema() error {
	for _, q := range sessionForkSchema {
		if _, err := a.db.Exec(q); err != nil {
			var dbErr *driver.MySQLError
			if errors.As(err, &dbErr) {
				switch dbErr.Number {
				case 1060, 1359:
					continue
				case 1419, 1142, 1143, 1227:
					return fmt.Errorf("session fork schema: database administrator action required: verify database-scoped schema privileges including CREATE, ALTER and TRIGGER; with binary logging enabled, configure log_bin_trust_function_creators=1 before startup; do not grant SUPER or global administrative privileges to the application account; the server will not change global settings or skip required triggers: %w", err)
				}
			}
			return fmt.Errorf("session fork schema: %w", err)
		}
	}
	return nil
}
