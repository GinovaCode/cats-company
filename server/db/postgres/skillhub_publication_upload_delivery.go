package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationUploadDeliveryStore = (*Adapter)(nil)

func (a *Adapter) ListSkillHubPublicationUploadDeliveries(ctx context.Context, s store.SkillHubPublicationRuntimeSelector, limit int) ([]string, error) {
	if s.Validate() != nil || limit < 1 || limit > 32 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT e.entry_id
		FROM skillhub_publication_entries e
		JOIN skillhub_publication_captures c USING(entry_id)
		JOIN users actor ON actor.id=e.actor_uid AND actor.account_type='human' AND actor.state=0
		JOIN users bot ON bot.id=e.bot_uid AND bot.account_type='bot' AND bot.state=0
		JOIN bot_config b ON b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id AND b.enabled=true
		LEFT JOIN skillhub_publication_cloud_receipts r USING(entry_id)
		JOIN skillhub_publication_upload_deliveries d USING(entry_id)
		WHERE r.entry_id IS NULL AND e.actor_uid=$1 AND e.bot_uid=$2 AND e.device_id=$3
		AND e.body_id=$4 AND e.installation_id=$5 AND e.state='waiting_runtime'
		AND ((d.state IN ('pending','uploaded') AND d.next_attempt_at<=clock_timestamp())
		  OR (d.state='leased' AND d.lease_until<=clock_timestamp()))
		ORDER BY e.created_at,e.entry_id LIMIT $6`,
		s.ActorUID, s.BotUID, s.DeviceID, s.BodyID, s.InstallationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (a *Adapter) ClaimSkillHubPublicationUploadDelivery(ctx context.Context, id string, b store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationUploadLease, error) {
	if !store.ValidSkillHubPublicationDeliveryEntryID(id) || b.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var entryState string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&entryState); errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	} else if err != nil {
		return nil, err
	}
	if entryState != "waiting_runtime" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	if err = lockPublicationDeliveryAuthority(ctx, tx, id, b); err != nil {
		return nil, err
	}
	capture, err := readPublicationCapture(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if capture == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if exists, err := publicationCloudReceiptExists(ctx, tx, id); err != nil {
		return nil, err
	} else if exists {
		return nil, store.ErrSkillHubPublicationSettled
	}
	var state string
	var attempts int
	var due bool
	err = tx.QueryRowContext(ctx, `SELECT state,attempts,
		CASE WHEN state='leased' THEN lease_until<=clock_timestamp() ELSE next_attempt_at<=clock_timestamp() END
		FROM skillhub_publication_upload_deliveries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&state, &attempts, &due)
	if errors.Is(err, sql.ErrNoRows) {
		state, attempts, due = "pending", 0, true
	} else if err != nil {
		return nil, err
	}
	if state == "needs_attention" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	if state != "pending" && state != "leased" && state != "uploaded" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	if !due {
		return nil, store.ErrSkillHubPublicationBusy
	}
	if attempts >= store.SkillHubPublicationDeliveryAttempts {
		if _, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_upload_deliveries
			SET state='needs_attention',last_code='exhausted',lease_until=clock_timestamp()
			WHERE entry_id=$1`, id); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, store.ErrSkillHubPublicationExhausted
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	lease := &store.SkillHubPublicationUploadLease{EntryID: id, Binding: b, Token: hex.EncodeToString(random[:]), Attempt: attempts + 1}
	if err = tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_upload_deliveries
		(entry_id,state,attempts,lease_token,lease_node,lease_connection,lease_generation,lease_until,next_attempt_at,last_code)
		VALUES($1,'leased',$2,$3,$4,$5,$6,clock_timestamp()+($7 * interval '1 second'),clock_timestamp(),'')
		ON CONFLICT(entry_id) DO UPDATE SET state='leased',attempts=EXCLUDED.attempts,lease_token=EXCLUDED.lease_token,
		lease_node=EXCLUDED.lease_node,lease_connection=EXCLUDED.lease_connection,lease_generation=EXCLUDED.lease_generation,
		lease_until=EXCLUDED.lease_until,next_attempt_at=EXCLUDED.next_attempt_at,last_code=''
		RETURNING lease_until`, id, lease.Attempt, lease.Token, b.NodeID, b.ConnectionID, b.Source.RouteGeneration,
		int(store.SkillHubPublicationDeliveryLease/time.Second)).Scan(&lease.ExpiresAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return lease, nil
}

func (a *Adapter) FinishSkillHubPublicationUploadDelivery(ctx context.Context, l store.SkillHubPublicationUploadLease, outcome string) error {
	if l.Validate() != nil || (outcome != "uploaded" && outcome != "retry" && outcome != "rejected") {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPublicationDeliveryAuthority(ctx, tx, l.EntryID, l.Binding); err != nil {
		return err
	}
	capture, err := readPublicationCapture(ctx, tx, l.EntryID)
	if err != nil {
		return err
	}
	if capture == nil {
		return store.ErrSkillHubPublicationDenied
	}
	if exists, err := publicationCloudReceiptExists(ctx, tx, l.EntryID); err != nil {
		return err
	} else if exists {
		outcome = "uploaded"
	}
	state, code := "pending", "retry"
	if outcome == "uploaded" {
		state, code = "uploaded", ""
	} else if outcome == "rejected" {
		state, code = "needs_attention", "rejected"
	} else if l.Attempt >= store.SkillHubPublicationDeliveryAttempts {
		state, code = "needs_attention", "exhausted"
	}
	res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_upload_deliveries SET state=$2,last_code=$3,
		next_attempt_at=clock_timestamp()+($4 * interval '1 second'),lease_until=clock_timestamp()
		WHERE entry_id=$1 AND state='leased' AND lease_token=$5 AND attempts=$6 AND lease_until=$7
		AND lease_node=$8 AND lease_connection=$9 AND lease_generation=$10 AND lease_until>clock_timestamp()`,
		l.EntryID, state, code, int(store.SkillHubPublicationDeliveryBackoff(l.Attempt)/time.Second),
		l.Token, l.Attempt, l.ExpiresAt, l.Binding.NodeID, l.Binding.ConnectionID, l.Binding.Source.RouteGeneration)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return store.ErrSkillHubPublicationStale
	}
	return tx.Commit()
}

func publicationCloudReceiptExists(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_cloud_receipts WHERE entry_id=$1)`, id).Scan(&exists)
	return exists, err
}
