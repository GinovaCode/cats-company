package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCancellationIntentStore = (*Adapter)(nil)

const publicationCancelIntentColumns = `c.cancel_id,c.entry_id,
	CASE WHEN NOT c.requires_cloud THEN 'local_only' WHEN r.entry_id IS NULL THEN 'waiting_receipt' ELSE 'ready' END,
	c.requested_at`

func scanPublicationCancellationIntent(row interface{ Scan(...any) error }) (*store.SkillHubPublicationCancellationIntent, error) {
	r := &store.SkillHubPublicationCancellationIntent{}
	err := row.Scan(&r.ID, &r.EntryID, &r.State, &r.RequestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (a *Adapter) RequestSkillHubPublicationCancellation(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationCancellationIntent, error) {
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
	e, err := lockPublicationCancellationAuthority(ctx, tx, actorUID, id)
	if err != nil {
		return nil, err
	}
	if e.State != "waiting_runtime" && e.State != "cancelled" {
		return nil, store.ErrSkillHubPublicationConflict
	}
	var releaseStarted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_description_release_permits WHERE entry_id=$1)`, id).Scan(&releaseStarted); err != nil {
		return nil, err
	}
	if releaseStarted {
		return nil, store.ErrSkillHubPublicationConflict
	}
	// The entry lock serializes with upload issuance/rotation, receipt recording
	// and local cancellation. Internal commit-permit issuance shares it; the
	// Cloud adapter must implement pending/too-late before either is enabled.
	// This state cancels LOCAL intent only; the distinct record below describes
	// outstanding Cloud work. Never project it as remote cancellation success.
	if e.State != "cancelled" {
		if _, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_entries SET state='cancelled' WHERE entry_id=$1`, id); err != nil {
			return nil, err
		}
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=$1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		// Resolve evidence while holding the entry lock, before INSERT. Reusing
		// one parameter as both a VARCHAR assignment and a TEXT comparison in
		// the INSERT subqueries produces PostgreSQL 42P08 type inference errors.
		var requiresCloud bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_upload_grants WHERE entry_id=$1)
			OR EXISTS(SELECT 1 FROM skillhub_publication_cloud_receipts WHERE entry_id=$1)`, id).Scan(&requiresCloud); err != nil {
			return nil, err
		}
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_cancel_intents(entry_id,cancel_id,requires_cloud)
			VALUES($1,$2,$3)`, id, "pubcancel_"+suffix, requiresCloud)
		if err != nil {
			return nil, err
		}
	}
	r, err := scanPublicationCancellationIntent(tx.QueryRowContext(ctx, `SELECT `+publicationCancelIntentColumns+`
		FROM skillhub_publication_cancel_intents c LEFT JOIN skillhub_publication_cloud_receipts r USING(entry_id)
		WHERE c.entry_id=$1`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func (a *Adapter) GetSkillHubPublicationCancellationIntent(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationCancellationIntent, error) {
	if actorUID <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// One committed snapshot, no Bot-owner join. A delayed receipt supplies the
	// original task reference without rewriting/re-authorizing the saved intent.
	return scanPublicationCancellationIntent(a.db.QueryRowContext(ctx, `SELECT `+publicationCancelIntentColumns+`
		FROM skillhub_publication_cancel_intents c JOIN skillhub_publication_entries e USING(entry_id)
		JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
		LEFT JOIN skillhub_publication_cloud_receipts r USING(entry_id)
		WHERE c.entry_id=$1 AND e.actor_uid=$2`, id, actorUID))
}
