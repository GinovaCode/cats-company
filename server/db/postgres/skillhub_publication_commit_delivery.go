package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCommitDeliveryStore = (*Adapter)(nil)

func (a *Adapter) ClaimSkillHubPublicationCommitDelivery(ctx context.Context, scope store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationCommitLease, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Existing trusted intents are the queue. Receipts without human intent can
	// never be selected. Scope is applied before LIMIT; no runtime-online join.
	var id string
	err = tx.QueryRowContext(ctx, `SELECT e.entry_id FROM skillhub_publication_entries e
 JOIN skillhub_publication_commit_intents i USING(entry_id)
 JOIN skillhub_publication_cloud_receipts r USING(entry_id)
 LEFT JOIN skillhub_publication_commit_deliveries d USING(entry_id)
 WHERE r.issuer=$1 AND r.audience=$2 AND (d.entry_id IS NULL OR
 (d.state='pending' AND d.next_attempt_at<=clock_timestamp()) OR (d.state='leased' AND d.lease_until<=clock_timestamp()))
 ORDER BY COALESCE(d.next_attempt_at,i.requested_at),e.entry_id LIMIT 1 FOR UPDATE OF e SKIP LOCKED`, scope.Issuer, scope.Audience).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil || r == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	i, err := readPublicationCommitIntent(ctx, tx, *r)
	if err != nil || i == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	var attempts int
	var state string
	var due, expired bool
	err = tx.QueryRowContext(ctx, `SELECT attempts,state,next_attempt_at<=clock_timestamp(),COALESCE(lease_until<=clock_timestamp(),false)
 FROM skillhub_publication_commit_deliveries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&attempts, &state, &due, &expired)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && !((state == "pending" && due) || (state == "leased" && expired)) {
		return nil, nil
	}
	if attempts >= 8 {
		_, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_commit_deliveries SET state='exhausted',lease_token=NULL,lease_until=NULL,last_code='retry' WHERE entry_id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	token, err := uploadRandomHex(16)
	if err != nil {
		return nil, err
	}
	l := &store.SkillHubPublicationCommitLease{Intent: *i, Token: token, Attempt: attempts + 1}
	err = tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_commit_deliveries(entry_id,state,attempts,lease_token,lease_until,next_attempt_at)
 VALUES($1,'leased',$2,$3,clock_timestamp()+interval '90 seconds',clock_timestamp())
 ON CONFLICT(entry_id) DO UPDATE SET state='leased',attempts=EXCLUDED.attempts,lease_token=EXCLUDED.lease_token,
 lease_until=EXCLUDED.lease_until,next_attempt_at=EXCLUDED.next_attempt_at RETURNING lease_until`, id, l.Attempt, token).Scan(&l.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

func (a *Adapter) FinishSkillHubPublicationCommitDelivery(ctx context.Context, l store.SkillHubPublicationCommitLease, o store.SkillHubPublicationCommitOutcome) error {
	if l.Validate() != nil || o.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := l.Intent.Input.Receipt.Query.EntryID
	if _, err = lockPublicationCommitEntry(ctx, tx, id); err != nil {
		return err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	if r == nil || *r != l.Intent.Input.Receipt {
		return store.ErrSkillHubPublicationStale
	}
	i, err := readPublicationCommitIntent(ctx, tx, *r)
	if err != nil {
		return err
	}
	if i == nil || *i != l.Intent {
		return store.ErrSkillHubPublicationStale
	}
	state, code := "needs_attention", o.Kind
	var permit any
	switch o.Kind {
	case "accepted":
		state, code, permit = "accepted", "", o.PermitID
	case "retry":
		state = "pending"
		if l.Attempt == 8 {
			state = "exhausted"
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_commit_deliveries SET state=$1,last_code=$2,permit_id=$3,
 lease_token=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+($4*interval '1 second')
 WHERE entry_id=$5 AND state='leased' AND lease_token=$6 AND attempts=$7 AND lease_until=$8 AND lease_until>clock_timestamp()`,
		state, code, permit, int(store.SkillHubPublicationDeliveryBackoff(l.Attempt).Seconds()), id, l.Token, l.Attempt, l.ExpiresAt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrSkillHubPublicationStale
	}
	return tx.Commit()
}

func (a *Adapter) GetSkillHubPublicationCommitDelivery(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationCommitDelivery, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s := &store.SkillHubPublicationCommitDelivery{}
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(d.state,'pending'),COALESCE(d.attempts,0),COALESCE(d.last_code,'')
 FROM skillhub_publication_commit_intents i JOIN skillhub_publication_entries e USING(entry_id)
 JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
 LEFT JOIN skillhub_publication_commit_deliveries d USING(entry_id)
 WHERE e.entry_id=$1 AND e.actor_uid=$2`, id, actor).Scan(&s.State, &s.Attempts, &s.LastCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}
