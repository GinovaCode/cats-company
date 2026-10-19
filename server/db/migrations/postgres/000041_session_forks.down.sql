DROP TRIGGER IF EXISTS session_fork_message_guard ON messages;
DROP FUNCTION IF EXISTS session_fork_message_guard();
DROP TABLE IF EXISTS session_forks;
ALTER TABLE topics DROP COLUMN IF EXISTS history_revision;
ALTER TABLE topics DROP COLUMN IF EXISTS fork_state;
