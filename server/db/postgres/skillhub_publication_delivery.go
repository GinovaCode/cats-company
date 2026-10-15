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

var _ store.SkillHubPublicationDeliveryStore = (*Adapter)(nil)

// This is an internal scheduler lookup, not an account-facing listing API.
// It returns references only; every claim independently checks live authority.
func (a *Adapter) ListSkillHubPublicationDeliveries(ctx context.Context, botUID int64, limit int) ([]string, error) {
	if botUID <= 0 || limit < 1 || limit > 32 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT o.entry_id FROM skillhub_publication_outbox o
		JOIN skillhub_publication_entries e ON e.entry_id=o.entry_id
		JOIN users actor ON actor.id=e.actor_uid AND actor.account_type='human' AND actor.state=0
		JOIN users bot ON bot.id=e.bot_uid AND bot.account_type='bot' AND bot.state=0
		JOIN bot_config b ON b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id AND b.enabled=true
		WHERE e.bot_uid=$1 AND o.kind='capture_candidate'
		AND ((o.delivery_state='pending' AND o.delivery_attempts<8 AND o.next_attempt_at<=clock_timestamp())
		OR (o.delivery_state='leased' AND o.lease_until<=clock_timestamp()))
		ORDER BY e.created_at, e.entry_id LIMIT $2`, botUID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// A new route generation is allowed only for the SAME immutable source binding.
// The caller must additionally prove the exact live socket/body lease; DB state
// cannot independently authenticate NodeID, ConnectionID or local references.
func lockPublicationDeliveryAuthority(ctx context.Context, tx *sql.Tx, id string, b store.SkillHubPublicationDeliveryBinding) error {
	e, err := scanPublicationEntry(tx.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+` FROM skillhub_publication_entries WHERE entry_id=$1`, id))
	if err != nil {
		return err
	}
	if e == nil {
		return store.ErrSkillHubPublicationDenied
	}
	expected := e.Input.Source
	if b.Source.RouteGeneration < expected.RouteGeneration {
		return store.ErrSkillHubPublicationStale
	}
	expected.RouteGeneration = b.Source.RouteGeneration
	if e.Input.ActorUID != b.ActorUID || e.Input.BotUID != b.BotUID || expected != b.Source {
		return store.ErrSkillHubPublicationDenied
	}
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND account_type='human' AND state=0 FOR SHARE`, b.ActorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT b.user_id FROM bot_config b JOIN users u ON u.id=b.user_id
		WHERE b.user_id=$1 AND b.owner_id=$2 AND b.body_id=$3 AND b.enabled=true
		AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u`, b.BotUID, b.ActorUID, b.Source.BodyID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return err
	}
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM bot_connection_generations WHERE bot_uid=$1 FOR SHARE`, b.BotUID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationStale
	}
	if err != nil {
		return err
	}
	if generation <= 0 || generation != b.Source.RouteGeneration {
		return store.ErrSkillHubPublicationStale
	}
	return nil
}

func beginPublicationDelivery(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='3s'; SET LOCAL statement_timeout='5s'`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (a *Adapter) ClaimSkillHubPublicationDelivery(ctx context.Context, id string, b store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationDeliveryLeaseRecord, error) {
	if !store.ValidSkillHubPublicationDeliveryEntryID(id) || b.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockPublicationDeliveryAuthority(ctx, tx, id, b); err != nil {
		return nil, err
	}
	var state string
	var attempts int
	var due bool
	err = tx.QueryRowContext(ctx, `SELECT delivery_state, delivery_attempts,
		CASE WHEN delivery_state='leased' THEN lease_until<=clock_timestamp() ELSE next_attempt_at<=clock_timestamp() END
		FROM skillhub_publication_outbox WHERE entry_id=$1 AND kind='capture_candidate' FOR UPDATE`, id).Scan(&state, &attempts, &due)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	if state == "source_verified" || state == "needs_user_action" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	if !due {
		return nil, store.ErrSkillHubPublicationBusy
	}
	if attempts >= store.SkillHubPublicationDeliveryAttempts {
		// The last worker may have crashed without saving its timeout. Keep the
		// exhausted event visible to the scheduler until this durable settlement.
		if _, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_outbox SET delivery_state='needs_user_action', last_code='publication.delivery_exhausted' WHERE entry_id=$1`, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, store.ErrSkillHubPublicationExhausted
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	lease := &store.SkillHubPublicationDeliveryLeaseRecord{EntryID: id, Token: hex.EncodeToString(random[:]), Binding: b}
	err = tx.QueryRowContext(ctx, `UPDATE skillhub_publication_outbox SET delivery_state='leased',
		delivery_attempts=delivery_attempts+1, lease_token=$2, lease_node=$3, lease_connection=$4,
		lease_generation=$5, lease_until=clock_timestamp()+($6 * INTERVAL '1 second'), last_code=''
		WHERE entry_id=$1 RETURNING delivery_attempts, lease_until`, id, lease.Token, b.NodeID, b.ConnectionID,
		b.Source.RouteGeneration, int(store.SkillHubPublicationDeliveryLease/time.Second)).Scan(&lease.Attempt, &lease.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return lease, nil
}

func (a *Adapter) FinishSkillHubPublicationDelivery(ctx context.Context, lease store.SkillHubPublicationDeliveryLeaseRecord, code string) error {
	state, valid := store.SkillHubPublicationDeliveryOutcome(code)
	if !valid || !store.ValidSkillHubPublicationDeliveryEntryID(lease.EntryID) || !store.ValidSkillHubPublicationDeliveryToken(lease.Token) || lease.Binding.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockPublicationDeliveryAuthority(ctx, tx, lease.EntryID, lease.Binding); err != nil {
		return err
	}
	var current, token, node, connection, lastCode string
	var generation int64
	var attempts int
	var expired bool
	err = tx.QueryRowContext(ctx, `SELECT delivery_state, COALESCE(lease_token,''), COALESCE(lease_node,''),
		COALESCE(lease_connection,''), COALESCE(lease_generation,0), delivery_attempts,
		COALESCE(lease_until<=clock_timestamp(),true), last_code
		FROM skillhub_publication_outbox WHERE entry_id=$1 FOR UPDATE`, lease.EntryID).
		Scan(&current, &token, &node, &connection, &generation, &attempts, &expired, &lastCode)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationStale
	}
	if err != nil {
		return err
	}
	if token != lease.Token || node != lease.Binding.NodeID || connection != lease.Binding.ConnectionID || generation != lease.Binding.Source.RouteGeneration {
		return store.ErrSkillHubPublicationStale
	}
	if state == "pending" && attempts >= store.SkillHubPublicationDeliveryAttempts {
		state, code = "needs_user_action", "publication.delivery_exhausted"
	}
	if current != "leased" {
		// Lost receipt-ack replay is harmless, but must match the already saved
		// result. It never extends a lease, resets attempts or changes authority.
		if current == state && lastCode == code {
			return tx.Commit()
		}
		return store.ErrSkillHubPublicationStale
	}
	if expired {
		return store.ErrSkillHubPublicationStale
	}
	result, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_outbox SET delivery_state=$2, last_code=$3,
		next_attempt_at=clock_timestamp()+($4 * INTERVAL '1 second'),
		source_verified_at=CASE WHEN $2='source_verified' THEN clock_timestamp() ELSE NULL END
		WHERE entry_id=$1 AND lease_token=$5 AND delivery_state='leased' AND lease_until>clock_timestamp()`,
		lease.EntryID, state, code, int(store.SkillHubPublicationDeliveryBackoff(attempts)/time.Second), lease.Token)
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
