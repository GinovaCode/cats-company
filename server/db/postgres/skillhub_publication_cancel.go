package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCancellationStore = (*Adapter)(nil)

func (a *Adapter) CancelSkillHubPublicationEntry(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationEntry, error) {
	if actorUID <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Same lock order as delivery: entry, human, Bot config. A receipt either
	// commits before this cancellation or observes cancelled and cannot save.
	e, err := scanPublicationEntry(tx.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+`
		FROM skillhub_publication_entries WHERE entry_id=$1 AND actor_uid=$2 FOR UPDATE`, id, actorUID))
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND account_type='human' AND state=0 FOR SHARE`, actorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	// Cancelling needs no online runtime, matching body or enabled Bot. It can
	// stop an old intent after the same Owner disables/rebinds their Bot, but
	// neither a new Owner nor an ex-Owner can mutate another ownership period.
	err = tx.QueryRowContext(ctx, `SELECT b.user_id FROM bot_config b JOIN users u ON u.id=b.user_id
		WHERE b.user_id=$1 AND b.owner_id=$2 AND u.account_type='bot' FOR SHARE OF b,u`, e.Input.BotUID, actorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	switch e.State {
	case "waiting_runtime":
		if _, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_entries SET state='cancelled' WHERE entry_id=$1`, id); err != nil {
			return nil, err
		}
		e.State = "cancelled"
	case "cancelled": // Same intent, idempotent success, even after reconnect.
	default:
		// Future capture/Cloud stages MUST implement their own cancellation and
		// commit-permit ordering. Never claim those tasks cancelled locally.
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return e, nil
}
