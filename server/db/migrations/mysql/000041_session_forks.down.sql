DROP TRIGGER IF EXISTS session_fork_message_insert;
DROP TRIGGER IF EXISTS session_fork_message_update;
DROP TRIGGER IF EXISTS session_fork_message_delete;
DROP TABLE IF EXISTS session_forks;
ALTER TABLE topics DROP COLUMN history_revision;
ALTER TABLE topics DROP COLUMN fork_state;
