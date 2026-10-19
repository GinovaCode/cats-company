package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCommitStore = (*Adapter)(nil)

// The receipt is immutable and retained independently. Never accept a new
// receipt here or infer task association from names or current Bot identity.
func readPublicationCommitReceipt(ctx context.Context, tx *sql.Tx, id string) (*store.SkillHubPublicationCloudReceipt, error) {
	r := &store.SkillHubPublicationCloudReceipt{}
	err := tx.QueryRowContext(ctx, `SELECT entry_id,issuer,audience,action,candidate_revision,acceptance_epoch,
		request_hash,source_snapshot_hash,request_bytes,delegation_id,idempotency_key,cloud_task_id,event_id
		FROM skillhub_publication_cloud_receipts WHERE entry_id=$1`, id).
		Scan(&r.Query.EntryID, &r.Query.Issuer, &r.Query.Audience, &r.Query.Action, &r.Query.Revision, &r.Query.Epoch,
			&r.Query.RequestHash, &r.Query.SourceSnapshotHash, &r.Query.RequestBytes, &r.DelegationID, &r.IdempotencyKey, &r.TaskID, &r.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func readPublicationCommitIntent(ctx context.Context, tx *sql.Tx, r store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCommitIntent, error) {
	i := &store.SkillHubPublicationCommitIntent{Input: store.SkillHubPublicationCommitInput{Receipt: r}}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT target,identity_hash,requested_at FROM skillhub_publication_commit_intents WHERE entry_id=$1`, r.Query.EntryID).
		Scan(&raw, &i.IdentityHash, &i.RequestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &i.Input.Target); err != nil {
		return nil, err
	}
	hash, err := i.Input.IdentityHash()
	if err != nil || hash != i.IdentityHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return i, nil
}

func readPublicationCommitPermit(ctx context.Context, tx *sql.Tx, i *store.SkillHubPublicationCommitIntent) (*store.SkillHubPublicationCommitPermit, error) {
	if i == nil {
		return nil, nil
	}
	p := &store.SkillHubPublicationCommitPermit{Intent: *i}
	err := tx.QueryRowContext(ctx, `SELECT permit_id,authority_epoch,authorized_at FROM skillhub_publication_commit_permits WHERE entry_id=$1`, i.Input.Receipt.Query.EntryID).
		Scan(&p.ID, &p.AuthorityEpoch, &p.AuthorizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// Shared ordering point with cancellation, upload renewal and receipt recording.
// Current human and Bot ownership/body/enabled state matter, not the transport
// generation: an already uploaded immutable candidate can finish while offline.
func lockPublicationCommitAuthority(ctx context.Context, tx *sql.Tx, e *store.SkillHubPublicationEntry) error {
	if e.State != "waiting_runtime" {
		return store.ErrSkillHubPublicationSettled
	}
	var uid int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND account_type='human' AND state=0 FOR SHARE`, e.Input.ActorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT b.user_id FROM bot_config b JOIN users u ON u.id=b.user_id
		WHERE b.user_id=$1 AND b.owner_id=$2 AND b.body_id=$3 AND b.enabled=true AND u.account_type='bot' AND u.state=0
		FOR SHARE OF b,u`, e.Input.BotUID, e.Input.ActorUID, e.Input.Source.BodyID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSkillHubPublicationDenied
	}
	return err
}

func lockPublicationCommitEntry(ctx context.Context, tx *sql.Tx, id string) (*store.SkillHubPublicationEntry, error) {
	e, err := scanPublicationEntry(tx.QueryRowContext(ctx, `SELECT `+publicationEntryColumns+` FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, id))
	if err == nil && e == nil {
		err = store.ErrSkillHubPublicationDenied
	}
	return e, err
}

func (a *Adapter) RecordSkillHubPublicationCommitIntent(ctx context.Context, actorUID int64, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitIntent, error) {
	hash, err := in.IdentityHash()
	if err != nil || actorUID <= 0 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	e, err := lockPublicationCommitEntry(ctx, tx, in.Receipt.Query.EntryID)
	if err != nil {
		return nil, err
	}
	if e.Input.ActorUID != actorUID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err := lockPublicationCommitAuthority(ctx, tx, e); err != nil {
		return nil, err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	if r == nil || *r != in.Receipt {
		return nil, store.ErrSkillHubPublicationConflict
	}
	i, err := readPublicationCommitIntent(ctx, tx, *r)
	if err != nil {
		return nil, err
	}
	if i != nil {
		if i.Input != in {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else {
		raw, err := json.Marshal(in.Target)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO skillhub_publication_commit_intents(entry_id,target,identity_hash) VALUES($1,$2,$3)`, e.ID, raw, hash); err != nil {
			return nil, err
		}
		i, err = readPublicationCommitIntent(ctx, tx, *r)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return i, nil
}

func (a *Adapter) AuthorizeSkillHubPublicationCommit(ctx context.Context, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
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
	e, err := lockPublicationCommitEntry(ctx, tx, in.Receipt.Query.EntryID)
	if err != nil {
		return nil, err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	if r == nil || *r != in.Receipt {
		return nil, store.ErrSkillHubPublicationConflict
	}
	i, err := readPublicationCommitIntent(ctx, tx, *r)
	if err != nil {
		return nil, err
	}
	if i == nil || i.Input != in {
		return nil, store.ErrSkillHubPublicationConflict
	}
	p, err := readPublicationCommitPermit(ctx, tx, i)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if err := lockPublicationCommitAuthority(ctx, tx, e); err != nil {
			return nil, err
		}
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, err
		}
		// Epoch records the latest upload authorization at the ordering point,
		// separate from the immutable first-acceptance epoch in the receipt.
		res, err := tx.ExecContext(ctx, `INSERT INTO skillhub_publication_commit_permits(entry_id,permit_id,authority_epoch)
			SELECT entry_id,$2,authority_epoch FROM skillhub_publication_upload_grants WHERE entry_id=$1`, e.ID, "pubpermit_"+suffix)
		if err != nil {
			return nil, err
		}
		if count, err := res.RowsAffected(); err != nil || count != 1 {
			return nil, store.ErrSkillHubPublicationConflict
		}
		p, err = readPublicationCommitPermit(ctx, tx, i)
		if err != nil {
			return nil, err
		}
	}
	// Exact replay is historical authorization, not new authority. It survives
	// local cancellation/transfer/disablement; Cloud cancellation can still win.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *Adapter) GetSkillHubPublicationCommitPermit(ctx context.Context, actorUID int64, id string) (*store.SkillHubPublicationCommitPermit, error) {
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
	// Human history is private to the original active actor, never a new owner.
	var uid int64
	// Keep the same entry -> human lock order as issuance/cancellation.
	err = tx.QueryRowContext(ctx, `SELECT actor_uid FROM skillhub_publication_entries
		WHERE entry_id=$1 AND actor_uid=$2 FOR SHARE`, id, actorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND account_type='human' AND state=0 FOR SHARE`, actorUID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil || r == nil {
		return nil, err
	}
	i, err := readPublicationCommitIntent(ctx, tx, *r)
	if err != nil {
		return nil, err
	}
	p, err := readPublicationCommitPermit(ctx, tx, i)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}
