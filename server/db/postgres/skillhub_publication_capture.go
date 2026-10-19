package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCaptureStore = (*Adapter)(nil)

func readPublicationCapture(ctx context.Context, tx *sql.Tx, id string) (*store.SkillHubPublicationCaptureRecord, error) {
	r := &store.SkillHubPublicationCaptureRecord{}
	in, b := &r.Input, &r.Input.Binding
	err := tx.QueryRowContext(ctx, `SELECT c.entry_id,c.candidate_revision,c.request_hash,c.source_snapshot_hash,c.request_bytes,
		c.captured_at,e.actor_uid,e.bot_uid,e.device_id,e.body_id,e.installation_id,e.workspace_generation,e.local_skill_id,
		c.route_generation,c.node_id,c.connection_id
		FROM skillhub_publication_captures c JOIN skillhub_publication_entries e ON e.entry_id=c.entry_id WHERE c.entry_id=$1`, id).
		Scan(&in.EntryID, &in.Revision, &in.RequestHash, &in.SourceSnapshotHash, &in.RequestBytes, &r.CapturedAt,
			&b.ActorUID, &b.BotUID, &b.Source.DeviceID, &b.Source.BodyID, &b.Source.InstallationID,
			&b.Source.WorkspaceGeneration, &b.Source.LocalSkillID, &b.Source.RouteGeneration, &b.NodeID, &b.ConnectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func lockPublicationVerifiedSource(ctx context.Context, tx *sql.Tx, id string) error {
	var verified bool
	err := tx.QueryRowContext(ctx, `SELECT delivery_state='source_verified' AND source_verified_at IS NOT NULL
		FROM skillhub_publication_outbox WHERE entry_id=$1 AND kind='capture_candidate' FOR SHARE`, id).Scan(&verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !verified) {
		return store.ErrSkillHubPublicationDenied
	}
	return err
}

// A reconnect can carry the same retained envelope. It cannot replace bytes or
// move the source to another Bot/device/body/install; same-generation socket
// substitution is also forbidden. Current authority is checked separately.
func samePublicationCapture(old, next store.SkillHubPublicationCaptureInput) bool {
	if next.Binding.Source.RouteGeneration < old.Binding.Source.RouteGeneration {
		return false
	}
	if next.Binding.Source.RouteGeneration == old.Binding.Source.RouteGeneration &&
		(next.Binding.NodeID != old.Binding.NodeID || next.Binding.ConnectionID != old.Binding.ConnectionID) {
		return false
	}
	old.Binding.Source.RouteGeneration = next.Binding.Source.RouteGeneration
	old.Binding.NodeID = next.Binding.NodeID
	old.Binding.ConnectionID = next.Binding.ConnectionID
	return old == next
}

func captureForUpload(in store.SkillHubPublicationUploadInput) store.SkillHubPublicationCaptureInput {
	return store.SkillHubPublicationCaptureInput{EntryID: in.EntryID, Binding: in.Binding, Revision: in.Revision,
		RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes}
}

// The entry row serializes first evidence, grant issuance and cancellation.
// DB authority checks cannot authenticate a live socket: that remains the
// responsibility of the trusted dispatch adapter, before invoking this method.
func (a *Adapter) RecordSkillHubPublicationCapture(ctx context.Context, in store.SkillHubPublicationCaptureInput) (*store.SkillHubPublicationCaptureRecord, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT entry_id FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, in.EntryID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	if err = lockPublicationDeliveryAuthority(ctx, tx, in.EntryID, in.Binding); err != nil {
		return nil, err
	}
	if err = lockPublicationVerifiedSource(ctx, tx, in.EntryID); err != nil {
		return nil, err
	}
	old, err := readPublicationCapture(ctx, tx, in.EntryID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if !samePublicationCapture(old.Input, in) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return old, nil
	}
	// Never infer evidence from a pre-migration grant. A real receipt may be
	// attached later, but it must agree with any already frozen candidate.
	grant, _, err := readUploadGrant(ctx, tx, in.EntryID)
	if err != nil {
		return nil, err
	}
	if grant != nil && !samePublicationCapture(captureForUpload(grant.Input), in) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_captures
		(entry_id,candidate_revision,request_hash,source_snapshot_hash,request_bytes,route_generation,node_id,connection_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, in.EntryID, in.Revision, in.RequestHash, in.SourceSnapshotHash, in.RequestBytes,
		in.Binding.Source.RouteGeneration, in.Binding.NodeID, in.Binding.ConnectionID)
	if err != nil {
		return nil, err
	}
	r, err := readPublicationCapture(ctx, tx, in.EntryID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}
