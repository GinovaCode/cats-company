package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationRepairDescriptionStore = (*Adapter)(nil)

func lockPublicationDescriptionTask(ctx context.Context, tx *sql.Tx, actor int64, id string) (*store.SkillHubPublicationRepairTask, error) {
	e, err := lockPublicationRepairAuthority(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	t, err := readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err := lockPublicationRepairTopic(ctx, tx, e, t.Input.TopicID); err != nil {
		return nil, err
	}
	return t, nil
}

func readPublicationDescription(ctx context.Context, tx *sql.Tx, t *store.SkillHubPublicationRepairTask) (*store.SkillHubPublicationDescriptionDraft, error) {
	if len(t.Rounds) == 0 {
		return nil, nil
	}
	d := &store.SkillHubPublicationDescriptionDraft{EntryID: t.Input.Receipt.Query.EntryID}
	var raw []byte
	var confirmed sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT d.input,d.confirmation_key,d.created_at,c.confirmed_at
		FROM skillhub_publication_description_drafts d
		LEFT JOIN skillhub_publication_description_confirmations c ON c.entry_id=d.entry_id AND c.round_number=d.round_number
		WHERE d.entry_id=$1 AND d.round_number=$2`, d.EntryID, len(t.Rounds)).Scan(&raw, &d.ConfirmationKey, &d.CreatedAt, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &d.Input) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if confirmed.Valid {
		d.ConfirmedAt = &confirmed.Time
	}
	if err := d.ValidateFor(*t); err != nil {
		return nil, err
	}
	return d, nil
}

// The verified-report adapter, not a browser/model, supplies the proposal.
// Retaining the draft and its waiting checkpoint is one fenced transaction.
func (a *Adapter) ProposeSkillHubPublicationDescription(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, p store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error) {
	if actor <= 0 || lease.Validate() != nil || p.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := lockPublicationDescriptionTask(ctx, tx, actor, lease.EntryID)
	if err != nil {
		return nil, err
	}
	if t.ID != lease.TaskID || len(t.Rounds) != lease.Round {
		return nil, store.ErrSkillHubPublicationStale
	}
	r := t.Rounds[lease.Round-1]
	stored, supplied := r.Lease, lease
	stored.Until, supplied.Until = time.Time{}, time.Time{}
	if stored != supplied || !r.Lease.Until.Equal(lease.Until) {
		return nil, store.ErrSkillHubPublicationStale
	}
	roundHash, err := r.Lease.Input.IdentityHash()
	if err != nil {
		return nil, err
	}
	input := store.SkillHubPublicationDescriptionDraftInput{TaskID: t.ID, TaskIdentityHash: t.IdentityHash, Round: lease.Round,
		RoundIdentityHash: roundHash, Evidence: lease.Input.Evidence, Proposal: p}
	key, err := input.IdentityHash()
	if err != nil {
		return nil, err
	}
	if r.Result != nil {
		d, err := readPublicationDescription(ctx, tx, t)
		if err != nil {
			return nil, err
		}
		if d == nil || d.ConfirmationKey != key {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return d, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_drafts
		(entry_id,round_number,input,confirmation_key,lease_worker,lease_token,lease_generation)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, lease.EntryID, lease.Round, raw, key, lease.WorkerID, lease.Token, lease.Generation)
	if err != nil {
		return nil, err
	}
	checkpoint := store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: lease.Input.Evidence}
	result, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_repair_rounds SET result=$3
		WHERE entry_id=$1 AND round_number=$2 AND result IS NULL AND lease_until>clock_timestamp()`, lease.EntryID, lease.Round, result)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return nil, store.ErrSkillHubPublicationStale
	}
	t.Rounds[lease.Round-1].Result = &checkpoint
	d, err := readPublicationDescription(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}

func (a *Adapter) GetSkillHubPublicationDescription(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionDraft, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := lockPublicationDescriptionTask(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	d, err := readPublicationDescription(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}

func (a *Adapter) ConfirmSkillHubPublicationDescription(ctx context.Context, actor int64, id, key string) (*store.SkillHubPublicationDescriptionDraft, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || !store.ValidSkillHubPublicationDescriptionKey(key) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := lockPublicationDescriptionTask(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	d, err := readPublicationDescription(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	if d == nil || d.ConfirmationKey != key {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if d.ConfirmedAt == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_confirmations(entry_id,round_number,confirmation_key,actor_uid)
			VALUES($1,$2,$3,$4)`, id, d.Input.Round, key, actor)
		if err != nil {
			return nil, err
		}
		d, err = readPublicationDescription(ctx, tx, t)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}
