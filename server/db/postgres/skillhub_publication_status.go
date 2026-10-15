package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationSourceStatusReader = (*Adapter)(nil)

func (a *Adapter) GetSkillHubPublicationSourceStatus(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationSourceStatus, error) {
	if actorUID <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s := &store.SkillHubPublicationSourceStatus{}
	// One statement observes a consistent committed ledger with database time.
	// Deliberately no current Bot-owner join: this is original-publisher history,
	// not authority to execute anything. It remains readable when workers stop.
	err := a.db.QueryRowContext(ctx, `SELECT e.entry_id, e.actor_uid,
		o.delivery_state, o.delivery_attempts, o.last_code, o.next_attempt_at,
		o.lease_until, o.source_verified_at, clock_timestamp()
		FROM skillhub_publication_entries e
		JOIN skillhub_publication_outbox o ON o.entry_id=e.entry_id AND o.kind='capture_candidate'
		JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
		WHERE e.entry_id=$1 AND e.actor_uid=$2`, id, actorUID).
		Scan(&s.EntryID, &s.ActorUID, &s.State, &s.Attempts, &s.LastCode, &s.NextAttemptAt,
			&s.LeaseUntil, &s.SourceVerifiedAt, &s.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}
