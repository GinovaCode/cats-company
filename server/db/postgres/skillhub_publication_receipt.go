package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCloudReceiptStore = (*Adapter)(nil)

func (a *Adapter) RecordSkillHubPublicationCloudReceipt(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCloudReceiptResult, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Same serialization point as renewal and local cancellation. Never hold
	// this lock across network I/O. A delayed callback must not revive intent.
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, r.Query.EntryID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	g, _, err := readUploadGrant(ctx, tx, r.Query.EntryID)
	if err != nil {
		return nil, err
	}
	// Historical evidence may arrive after expiry, renewal, disconnect, owner
	// transfer or cancellation. Match retained immutable scope, not live token
	// validity. No receipt can invent a grant or a not-yet-issued epoch.
	if g == nil || g.ID != r.DelegationID || g.IdempotencyKey != r.IdempotencyKey ||
		g.Epoch < r.Query.Epoch || g.Input.Query(r.Query.Epoch) != r.Query {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if state != "waiting_runtime" && state != "cancelled" {
		return nil, store.ErrSkillHubPublicationConflict
	}
	var prior store.SkillHubPublicationCloudReceipt
	err = tx.QueryRowContext(ctx, `SELECT entry_id,issuer,audience,action,candidate_revision,acceptance_epoch,
		request_hash,source_snapshot_hash,request_bytes,delegation_id,idempotency_key,cloud_task_id,event_id
		FROM skillhub_publication_cloud_receipts WHERE entry_id=$1`, r.Query.EntryID).
		Scan(&prior.Query.EntryID, &prior.Query.Issuer, &prior.Query.Audience, &prior.Query.Action,
			&prior.Query.Revision, &prior.Query.Epoch, &prior.Query.RequestHash, &prior.Query.SourceSnapshotHash,
			&prior.Query.RequestBytes, &prior.DelegationID, &prior.IdempotencyKey, &prior.TaskID, &prior.EventID)
	if err == nil {
		if prior != r {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_cloud_receipts
			(entry_id,issuer,audience,action,candidate_revision,acceptance_epoch,request_hash,source_snapshot_hash,
			request_bytes,delegation_id,idempotency_key,cloud_task_id,event_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			r.Query.EntryID, r.Query.Issuer, r.Query.Audience, r.Query.Action, r.Query.Revision, r.Query.Epoch,
			r.Query.RequestHash, r.Query.SourceSnapshotHash, r.Query.RequestBytes, r.DelegationID, r.IdempotencyKey, r.TaskID, r.EventID)
		if err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				return nil, store.ErrSkillHubPublicationConflict
			}
			return nil, err
		}
	} else {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &store.SkillHubPublicationCloudReceiptResult{Receipt: r, LocalIntentCancelled: state == "cancelled"}, nil
}
