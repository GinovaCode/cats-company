package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationUploadGrantStore = (*Adapter)(nil)

const uploadGrantColumns = `g.grant_id, g.entry_id, g.issuer, g.audience, g.action,
	g.candidate_revision, g.request_hash, g.source_snapshot_hash, g.request_bytes,
	g.idempotency_key, g.authority_epoch, g.created_at, g.issued_at, g.expires_at,
	g.token_hash, e.actor_uid, e.bot_uid, e.device_id, e.body_id, e.installation_id,
	g.route_generation, e.workspace_generation, e.local_skill_id, g.node_id, g.connection_id`

func readUploadGrant(ctx context.Context, tx *sql.Tx, id string) (*store.SkillHubPublicationUploadGrant, string, error) {
	g := &store.SkillHubPublicationUploadGrant{}
	in := &g.Input
	b := &in.Binding
	var hash string
	err := tx.QueryRowContext(ctx, `SELECT `+uploadGrantColumns+`
		FROM skillhub_publication_upload_grants g JOIN skillhub_publication_entries e ON e.entry_id=g.entry_id
		WHERE g.entry_id=$1`, id).Scan(&g.ID, &in.EntryID, &in.Issuer, &in.Audience, &in.Action,
		&in.Revision, &in.RequestHash, &in.SourceSnapshotHash, &in.RequestBytes, &g.IdempotencyKey,
		&g.Epoch, &g.CreatedAt, &g.IssuedAt, &g.ExpiresAt, &hash, &b.ActorUID, &b.BotUID,
		&b.Source.DeviceID, &b.Source.BodyID, &b.Source.InstallationID, &b.Source.RouteGeneration,
		&b.Source.WorkspaceGeneration, &b.Source.LocalSkillID, &b.NodeID, &b.ConnectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	return g, hash, err
}

func uploadRandomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func uploadTokenHash(token store.SkillHubPublicationUploadToken) string {
	h := sha256.Sum256([]byte(token.Reveal()))
	return hex.EncodeToString(h[:])
}

// The entry row is the common ordering point for issuance, rotation and cancel.
// No byte buffers, network work or model calls occur inside this transaction.
func (a *Adapter) IssueSkillHubPublicationUploadGrant(ctx context.Context, in store.SkillHubPublicationUploadInput) (*store.SkillHubPublicationUploadGrant, store.SkillHubPublicationUploadToken, error) {
	var empty store.SkillHubPublicationUploadToken
	if err := in.Validate(); err != nil {
		return nil, empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, empty, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT entry_id FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, in.EntryID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, empty, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, empty, err
	}
	if err := lockPublicationDeliveryAuthority(ctx, tx, in.EntryID, in.Binding); err != nil {
		return nil, empty, err
	}
	var verified bool
	err = tx.QueryRowContext(ctx, `SELECT delivery_state='source_verified' AND source_verified_at IS NOT NULL
		FROM skillhub_publication_outbox WHERE entry_id=$1 AND kind='capture_candidate' FOR SHARE`, in.EntryID).Scan(&verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !verified) {
		return nil, empty, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, empty, err
	}
	old, _, err := readUploadGrant(ctx, tx, in.EntryID)
	if err != nil {
		return nil, empty, err
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, empty, err
	}
	if old != nil {
		// Only a verified reconnect may change the transport generation/socket.
		// Candidate bytes, issuer, audience, revision and stable upload key freeze
		// at first issue. Renewing a token cannot substitute a new candidate.
		expected := old.Input
		expected.Binding.Source.RouteGeneration = in.Binding.Source.RouteGeneration
		expected.Binding.NodeID = in.Binding.NodeID
		expected.Binding.ConnectionID = in.Binding.ConnectionID
		if expected != in || in.Binding.Source.RouteGeneration < old.Input.Binding.Source.RouteGeneration ||
			(in.Binding.Source.RouteGeneration == old.Input.Binding.Source.RouteGeneration &&
				(in.Binding.NodeID != old.Input.Binding.NodeID || in.Binding.ConnectionID != old.Input.Binding.ConnectionID)) {
			return nil, empty, store.ErrSkillHubPublicationConflict
		}
		if old.Epoch >= store.SkillHubPublicationUploadMaxIssues {
			return nil, empty, store.ErrSkillHubPublicationExhausted
		}
		if now.Before(old.IssuedAt.Add(store.SkillHubPublicationUploadCooldown)) {
			return nil, empty, store.ErrSkillHubPublicationRateLimit
		}
	}
	secret, err := uploadRandomHex(32)
	if err != nil {
		return nil, empty, err
	}
	token, err := store.ParseSkillHubPublicationUploadToken("pubupload_" + secret)
	if err != nil {
		return nil, empty, err
	}
	if old == nil {
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, empty, err
		}
		key, err := uploadRandomHex(16)
		if err != nil {
			return nil, empty, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_upload_grants
			(entry_id,grant_id,issuer,audience,action,candidate_revision,request_hash,source_snapshot_hash,request_bytes,
			idempotency_key,token_hash,authority_epoch,route_generation,node_id,connection_id,issued_at,expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1,$12,$13,$14,$15,$16)`,
			in.EntryID, "pubgrant_"+suffix, in.Issuer, in.Audience, in.Action, in.Revision, in.RequestHash, in.SourceSnapshotHash,
			in.RequestBytes, "pubcandidate_"+key, uploadTokenHash(token), in.Binding.Source.RouteGeneration,
			in.Binding.NodeID, in.Binding.ConnectionID, now, now.Add(store.SkillHubPublicationUploadTTL))
		if err != nil {
			return nil, empty, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_upload_grants SET token_hash=$2,authority_epoch=authority_epoch+1,
			route_generation=$3,node_id=$4,connection_id=$5,issued_at=$6,expires_at=$7 WHERE entry_id=$1`,
			in.EntryID, uploadTokenHash(token), in.Binding.Source.RouteGeneration, in.Binding.NodeID, in.Binding.ConnectionID,
			now, now.Add(store.SkillHubPublicationUploadTTL))
		if err != nil {
			return nil, empty, err
		}
	}
	g, _, err := readUploadGrant(ctx, tx, in.EntryID)
	if err != nil {
		return nil, empty, err
	}
	if err := tx.Commit(); err != nil {
		return nil, empty, err
	}
	return g, token, nil
}

// Internal online authority observation, not a durable commit permit or a
// guarantee that authority will remain valid after this transaction. Cloud task
// acceptance and cancellation ordering MUST be connected before external use.
func (a *Adapter) CheckSkillHubPublicationUploadGrant(ctx context.Context, token store.SkillHubPublicationUploadToken, expected store.SkillHubPublicationUploadInput, epoch int) (*store.SkillHubPublicationUploadGrant, error) {
	if expected.Validate() != nil || epoch < 1 || epoch > store.SkillHubPublicationUploadMaxIssues {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if _, err := store.ParseSkillHubPublicationUploadToken(token.Reveal()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockPublicationDeliveryAuthority(ctx, tx, expected.EntryID, expected.Binding); err != nil {
		return nil, err
	}
	g, hash, err := readUploadGrant(ctx, tx, expected.EntryID)
	if err != nil {
		return nil, err
	}
	if g == nil || g.Input != expected || g.Epoch != epoch ||
		subtle.ConstantTimeCompare([]byte(hash), []byte(uploadTokenHash(token))) != 1 {
		return nil, store.ErrSkillHubPublicationDenied
	}
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp() >= $1 AND clock_timestamp() < $2`, g.IssuedAt, g.ExpiresAt).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}
