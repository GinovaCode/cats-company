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

var _ store.SkillHubPublicationEntryStore = (*Adapter)(nil)

const publicationEntryColumns = `entry_id, request_id, actor_uid, bot_uid, entrypoint,
	device_id, body_id, installation_id, route_generation, workspace_generation,
	local_skill_id, identity_hash, state, created_at`

func scanPublicationEntry(row interface{ Scan(...any) error }) (*store.SkillHubPublicationEntry, error) {
	e := &store.SkillHubPublicationEntry{}
	err := row.Scan(&e.ID, &e.Input.RequestID, &e.Input.ActorUID, &e.Input.BotUID, &e.Input.Entrypoint,
		&e.Input.Source.DeviceID, &e.Input.Source.BodyID, &e.Input.Source.InstallationID,
		&e.Input.Source.RouteGeneration, &e.Input.Source.WorkspaceGeneration, &e.Input.Source.LocalSkillID,
		&e.IdentityHash, &e.State, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (a *Adapter) CreateSkillHubPublicationEntry(ctx context.Context, in store.SkillHubPublicationEntryInput,
	policy store.SkillHubPublicationEntryPolicy) (*store.SkillHubPublicationEntry, bool, error) {
	hash, err := in.IdentityHash()
	if err != nil {
		return nil, false, err
	}
	if err := policy.Validate(); err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Serialize quota checks and insertion across processes. No network, file IO
	// or worker execution is allowed under this short admission transaction.
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, false, err
	}
	var lockID int
	if err = tx.QueryRowContext(ctx, `SELECT lock_id FROM skillhub_publication_admission WHERE lock_id=1 FOR UPDATE`).Scan(&lockID); err != nil {
		return nil, false, err
	}
	// Retain the locks through insertion: owner transfer, account disablement or
	// durable body rebinding cannot race between this check and the saved entry.
	var actor int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND account_type='human' AND state=0 FOR SHARE`, in.ActorUID).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, false, err
	}
	var bot int64
	err = tx.QueryRowContext(ctx, `SELECT b.user_id FROM bot_config b JOIN users u ON u.id=b.user_id
		WHERE b.user_id=$1 AND b.owner_id=$2 AND b.body_id=$3 AND b.enabled=true
		AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u`, in.BotUID, in.ActorUID, in.Source.BodyID).Scan(&bot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, false, err
	}
	previous, err := scanPublicationEntry(tx.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+
		` FROM skillhub_publication_entries WHERE actor_uid=$1 AND request_id=$2`, in.ActorUID, in.RequestID))
	if err != nil {
		return nil, false, err
	}
	if previous != nil {
		if previous.IdentityHash != hash || previous.Input != in {
			return nil, false, store.ErrSkillHubPublicationConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return previous, true, nil
	}
	var total, owned, recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE actor_uid=$1),
		COUNT(*) FILTER (WHERE actor_uid=$1 AND created_at >= clock_timestamp() - INTERVAL '1 minute')
		FROM skillhub_publication_entries`, in.ActorUID).Scan(&total, &owned, &recent); err != nil {
		return nil, false, err
	}
	if total >= policy.MaxEntries || owned >= policy.MaxEntriesPerActor {
		return nil, false, store.ErrSkillHubPublicationCapacity
	}
	if recent >= policy.MaxCreatesPerMinute {
		return nil, false, store.ErrSkillHubPublicationRateLimit
	}
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return nil, false, err
	}
	id := "pubentry_" + hex.EncodeToString(randomID[:])
	e, err := scanPublicationEntry(tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_entries
		(entry_id, request_id, actor_uid, bot_uid, entrypoint, device_id, body_id, installation_id,
		route_generation, workspace_generation, local_skill_id, identity_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+publicationEntryColumns,
		id, in.RequestID, in.ActorUID, in.BotUID, in.Entrypoint, in.Source.DeviceID, in.Source.BodyID,
		in.Source.InstallationID, in.Source.RouteGeneration, in.Source.WorkspaceGeneration, in.Source.LocalSkillID, hash))
	if err != nil {
		return nil, false, err
	}
	// Reference-only event: no paths, candidate bytes, tokens or model input.
	if _, err := tx.ExecContext(ctx, `INSERT INTO skillhub_publication_outbox (entry_id, kind) VALUES ($1,'capture_candidate')`, id); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return e, false, nil
}

// Reads remain private to the original human publisher even after Bot transfer.
// This returns historical intent, not permission for a new runtime operation.
func (a *Adapter) GetSkillHubPublicationEntry(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationEntry, error) {
	if actorUID <= 0 || len(id) != 41 || id[:9] != "pubentry_" {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if _, err := hex.DecodeString(id[9:]); err != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return scanPublicationEntry(a.db.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+`
		FROM skillhub_publication_entries WHERE entry_id=$1 AND actor_uid=$2
		AND EXISTS (SELECT 1 FROM users WHERE id=$2 AND account_type='human' AND state=0)`, id, actorUID))
}
