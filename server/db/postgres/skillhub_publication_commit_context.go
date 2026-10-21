package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCommitContextReader = (*Adapter)(nil)

func (a *Adapter) GetSkillHubPublicationCommitContext(ctx context.Context, actor int64, id string, current bool) (*store.SkillHubPublicationCommitContext, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Consistent read snapshot, no locks held during the later Cloud call.
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	e, err := scanPublicationEntry(tx.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+` FROM skillhub_publication_entries e
		WHERE entry_id=$1 AND actor_uid=$2 AND EXISTS(SELECT 1 FROM users u WHERE u.id=e.actor_uid AND u.account_type='human' AND u.state=0)`, id, actor))
	if err != nil || e == nil {
		return nil, err
	}
	if current {
		if e.State != "waiting_runtime" {
			return nil, store.ErrSkillHubPublicationSettled
		}
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bot_config b JOIN users u ON u.id=b.user_id
			WHERE b.user_id=$1 AND b.owner_id=$2 AND b.body_id=$3 AND b.enabled=true AND u.account_type='bot' AND u.state=0)`, e.Input.BotUID, actor, e.Input.Source.BodyID).Scan(&allowed)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, store.ErrSkillHubPublicationDenied
		}
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	out := &store.SkillHubPublicationCommitContext{Entry: *e, Receipt: r}
	if r != nil {
		if r.Validate() != nil {
			return nil, store.ErrSkillHubPublicationConflict
		}
		out.Intent, err = readPublicationCommitIntent(ctx, tx, *r)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
