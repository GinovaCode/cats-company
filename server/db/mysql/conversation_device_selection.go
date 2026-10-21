package mysql

import (
	"context"
	"database/sql"
	"errors"

	driver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
)

var _ store.ConversationDeviceSelectionStore = (*Adapter)(nil)

func (a *Adapter) GetConversationDeviceSelection(ctx context.Context, actor int64, session string) (*store.ConversationDeviceSelection, error) {
	if err := store.ValidateDeviceSelectionScope(actor, session); err != nil {
		return nil, err
	}
	var record store.ConversationDeviceSelection
	err := a.db.QueryRowContext(ctx, `SELECT device_id, revision, source, updated_at_ms FROM conversation_device_selections WHERE actor_uid = ? AND session_key = ?`, actor, session).
		Scan(&record.DeviceID, &record.Revision, &record.Source, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (a *Adapter) CompareConversationDeviceSelection(ctx context.Context, actor int64, session, expected string, next store.ConversationDeviceSelection) (bool, error) {
	if err := store.ValidateDeviceSelectionScope(actor, session); err != nil {
		return false, err
	}
	if err := store.ValidateDeviceSelectionRecord(expected, next); err != nil {
		return false, err
	}
	var result sql.Result
	var err error
	if expected == "" {
		// INSERT IGNORE can hide non-duplicate data errors. An upsert's affected
		// row count also depends on clientFoundRows. Only a real insert wins.
		result, err = a.db.ExecContext(ctx, `INSERT INTO conversation_device_selections (actor_uid, session_key, device_id, revision, source, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?)`, actor, session, next.DeviceID, next.Revision, next.Source, next.UpdatedAt)
		var duplicate *driver.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return false, nil
		}
	} else {
		result, err = a.db.ExecContext(ctx, `UPDATE conversation_device_selections SET device_id = ?, revision = ?, source = ?, updated_at_ms = ? WHERE actor_uid = ? AND session_key = ? AND revision = ?`, next.DeviceID, next.Revision, next.Source, next.UpdatedAt, actor, session, expected)
	}
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

const createConversationDeviceSelectionsTable = `
CREATE TABLE IF NOT EXISTS conversation_device_selections (
    actor_uid BIGINT NOT NULL,
    session_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    device_id VARCHAR(128) NOT NULL,
    revision VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source VARCHAR(64) NOT NULL,
    updated_at_ms BIGINT NOT NULL,
    PRIMARY KEY (actor_uid, session_key),
    CONSTRAINT fk_device_selection_actor FOREIGN KEY (actor_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`
