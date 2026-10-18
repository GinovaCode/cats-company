package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCloudStatusReader = (*Adapter)(nil)

func (a *Adapter) GetSkillHubPublicationCloudStatus(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationCloudStatus, error) {
	if actorUID <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s := &store.SkillHubPublicationCloudStatus{}
	// One statement, one committed snapshot, no current Bot-owner join and no
	// peer call. A missing receipt is an observation gap, not a negative ACK.
	err := a.db.QueryRowContext(ctx, `SELECT e.entry_id,e.actor_uid,e.state,r.recorded_at,clock_timestamp()
		FROM skillhub_publication_entries e
		JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
		LEFT JOIN skillhub_publication_cloud_receipts r ON r.entry_id=e.entry_id
		WHERE e.entry_id=$1 AND e.actor_uid=$2`, id, actorUID).
		Scan(&s.EntryID, &s.ActorUID, &s.LocalIntentState, &s.ReceiptRecordedAt, &s.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}
