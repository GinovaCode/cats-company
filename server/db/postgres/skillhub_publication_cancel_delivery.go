package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCancellationDeliveryStore = (*Adapter)(nil)

const cancelReceiptColumns = `r.entry_id,r.issuer,r.audience,r.action,r.candidate_revision,r.acceptance_epoch,
 r.request_hash,r.source_snapshot_hash,r.request_bytes,r.delegation_id,r.idempotency_key,r.cloud_task_id,r.event_id`

func readCancellationReceipt(ctx context.Context, tx *sql.Tx, id string) (string, store.SkillHubPublicationCloudReceipt, error) {
	var cancelID string
	var r store.SkillHubPublicationCloudReceipt
	err := tx.QueryRowContext(ctx, `SELECT c.cancel_id,`+cancelReceiptColumns+`
 FROM skillhub_publication_cancel_intents c JOIN skillhub_publication_cloud_receipts r USING(entry_id)
 WHERE c.entry_id=$1 AND c.requires_cloud`, id).Scan(&cancelID, &r.Query.EntryID, &r.Query.Issuer, &r.Query.Audience,
		&r.Query.Action, &r.Query.Revision, &r.Query.Epoch, &r.Query.RequestHash, &r.Query.SourceSnapshotHash,
		&r.Query.RequestBytes, &r.DelegationID, &r.IdempotencyKey, &r.TaskID, &r.EventID)
	return cancelID, r, err
}

func (a *Adapter) ClaimSkillHubPublicationCancellation(ctx context.Context, scope store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationCancellationLease, error) {
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
	// Filter the configured peer BEFORE LIMIT. No live owner/runtime join: this
	// only delivers an already authorized immutable cancellation, even after transfer.
	var id string
	err = tx.QueryRowContext(ctx, `SELECT e.entry_id FROM skillhub_publication_entries e
 JOIN skillhub_publication_cancel_intents c USING(entry_id)
 JOIN skillhub_publication_cloud_receipts r USING(entry_id)
 LEFT JOIN skillhub_publication_cancel_deliveries d USING(entry_id)
 WHERE e.state='cancelled' AND c.requires_cloud AND r.issuer=$1 AND r.audience=$2
 AND (d.entry_id IS NULL OR (d.state='pending' AND d.next_attempt_at<=clock_timestamp())
 OR (d.state='leased' AND d.lease_until<=clock_timestamp()))
 ORDER BY COALESCE(d.next_attempt_at,c.requested_at),e.entry_id LIMIT 1 FOR UPDATE OF e SKIP LOCKED`, scope.Issuer, scope.Audience).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cancelID, receipt, err := readCancellationReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var attempts int
	var state string
	var due, expired bool
	err = tx.QueryRowContext(ctx, `SELECT attempts,state,next_attempt_at<=clock_timestamp(),COALESCE(lease_until<=clock_timestamp(),false)
 FROM skillhub_publication_cancel_deliveries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&attempts, &state, &due, &expired)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && !((state == "pending" && due) || (state == "leased" && expired)) {
		return nil, nil
	}
	if attempts >= 8 {
		_, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_cancel_deliveries SET state='exhausted',lease_token=NULL,lease_until=NULL,last_code='retry' WHERE entry_id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	token, err := uploadRandomHex(16)
	if err != nil {
		return nil, err
	}
	l := &store.SkillHubPublicationCancellationLease{CancellationID: cancelID, Receipt: receipt, Token: token, Attempt: attempts + 1}
	err = tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_cancel_deliveries(entry_id,state,attempts,lease_token,lease_until,next_attempt_at)
 VALUES($1,'leased',$2,$3,clock_timestamp()+interval '30 seconds',clock_timestamp())
 ON CONFLICT(entry_id) DO UPDATE SET state='leased',attempts=EXCLUDED.attempts,lease_token=EXCLUDED.lease_token,
 lease_until=EXCLUDED.lease_until,next_attempt_at=EXCLUDED.next_attempt_at
 RETURNING lease_until`, id, l.Attempt, token).Scan(&l.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

func (a *Adapter) FinishSkillHubPublicationCancellation(ctx context.Context, l store.SkillHubPublicationCancellationLease, outcome store.SkillHubPublicationCancellationOutcome) error {
	if l.Validate() != nil || outcome.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, l.Receipt.Query.EntryID).Scan(&state); err != nil {
		return err
	}
	if state != "cancelled" {
		return store.ErrSkillHubPublicationStale
	}
	id, receipt, err := readCancellationReceipt(ctx, tx, l.Receipt.Query.EntryID)
	if err != nil {
		return err
	}
	if id != l.CancellationID || receipt != l.Receipt {
		return store.ErrSkillHubPublicationStale
	}
	nextState, lastCode := "pending", outcome.Kind
	var cancelledAt, observedAt any
	switch outcome.Kind {
	case "confirmed":
		nextState, lastCode = "confirmed", ""
		cancelledAt, observedAt = outcome.CancelledAt, outcome.ObservedAt
	case "conflict", "not_found":
		nextState = "needs_attention"
	case "retry":
		if l.Attempt >= 8 {
			nextState = "exhausted"
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_cancel_deliveries SET state=$1,last_code=$2,
 remote_cancelled_at=$3,remote_observed_at=$4,lease_token=NULL,lease_until=NULL,
 next_attempt_at=clock_timestamp()+($5*interval '1 second')
 WHERE entry_id=$6 AND state='leased' AND lease_token=$7 AND attempts=$8 AND lease_until=$9 AND lease_until>clock_timestamp()`,
		nextState, lastCode, cancelledAt, observedAt, int(store.SkillHubPublicationDeliveryBackoff(l.Attempt).Seconds()), l.Receipt.Query.EntryID, l.Token, l.Attempt, l.ExpiresAt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrSkillHubPublicationStale
	}
	return tx.Commit()
}

func (a *Adapter) GetSkillHubPublicationCancellationDelivery(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationCancellationDelivery, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := &store.SkillHubPublicationCancellationDelivery{}
	err := a.db.QueryRowContext(ctx, `SELECT c.cancel_id,d.state,d.attempts,d.last_code,d.remote_cancelled_at,d.remote_observed_at
 FROM skillhub_publication_cancel_deliveries d JOIN skillhub_publication_cancel_intents c USING(entry_id)
 JOIN skillhub_publication_entries e USING(entry_id)
 JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
 WHERE d.entry_id=$1 AND e.actor_uid=$2`, id, actor).Scan(&r.CancellationID, &r.State, &r.Attempts, &r.LastCode, &r.CancelledAt, &r.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}
