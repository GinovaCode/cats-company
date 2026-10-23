package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionApplicationStore = (*Adapter)(nil)

func publicationDescriptionApplicationContext(ctx context.Context, tx *sql.Tx, actor int64, id string) (*store.SkillHubPublicationRepairTask, *store.SkillHubPublicationDescriptionDraft, error) {
	t, err := lockPublicationDescriptionTask(ctx, tx, actor, id)
	if err != nil {
		return nil, nil, err
	}
	d, err := readPublicationDescription(ctx, tx, t)
	if err != nil {
		return nil, nil, err
	}
	if d == nil || d.ConfirmedAt == nil {
		return nil, nil, store.ErrSkillHubPublicationDenied
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if r == nil || *r != t.Input.Receipt {
		return nil, nil, store.ErrSkillHubPublicationConflict
	}
	return t, d, nil
}

func readPublicationDescriptionApplication(ctx context.Context, tx *sql.Tx, t *store.SkillHubPublicationRepairTask, d *store.SkillHubPublicationDescriptionDraft) (*store.SkillHubPublicationDescriptionApplication, error) {
	a := &store.SkillHubPublicationDescriptionApplication{}
	var input, result []byte
	var applied sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT application_id,input,identity_hash,created_at,result,applied_at
		FROM skillhub_publication_description_applications WHERE entry_id=$1 AND round_number=$2`, d.EntryID, d.Input.Round).
		Scan(&a.ID, &input, &a.IdentityHash, &a.CreatedAt, &result, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(input, &a.Input) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if result != nil {
		a.Result = &store.SkillHubPublicationDescriptionApplicationResult{}
		if json.Unmarshal(result, a.Result) != nil {
			return nil, store.ErrSkillHubPublicationConflict
		}
	}
	if applied.Valid {
		a.AppliedAt = &applied.Time
	}
	if err := a.ValidateFor(*t, *d); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) descriptionApplication(ctx context.Context, actor int64, id string, prepare bool) (*store.SkillHubPublicationDescriptionApplication, error) {
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
	t, d, err := publicationDescriptionApplicationContext(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	app, err := readPublicationDescriptionApplication(ctx, tx, t, d)
	if err != nil {
		return nil, err
	}
	if app == nil && prepare {
		input := store.SkillHubPublicationDescriptionApplicationInput{Receipt: t.Input.Receipt, Draft: d.Input, ConfirmationKey: d.ConfirmationKey}
		h, err := input.IdentityHash()
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_applications
			(entry_id,round_number,confirmation_key,application_id,input,identity_hash) VALUES($1,$2,$3,$4,$5,$6)`,
			id, d.Input.Round, d.ConfirmationKey, "pubdesc_"+h, raw, h)
		if err != nil {
			return nil, err
		}
		app, err = readPublicationDescriptionApplication(ctx, tx, t, d)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *Adapter) PrepareSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	return a.descriptionApplication(ctx, actor, id, true)
}

func (a *Adapter) GetSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	return a.descriptionApplication(ctx, actor, id, false)
}

// Only the trusted derive-and-retain adapter may supply this completion. Exact
// metadata replay recovers a lost response; it never permits another candidate
// to replace the first result. Current authority is checked even on replay.
func (a *Adapter) CompleteSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionApplication, result store.SkillHubPublicationDescriptionApplicationResult) (*store.SkillHubPublicationDescriptionApplication, error) {
	h, err := expected.Input.IdentityHash()
	if actor <= 0 || err != nil || expected.IdentityHash != h || expected.ID != "pubdesc_"+h || result.ValidateFor(expected) != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := expected.Input.Receipt.Query.EntryID
	t, d, err := publicationDescriptionApplicationContext(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	app, err := readPublicationDescriptionApplication(ctx, tx, t, d)
	if err != nil {
		return nil, err
	}
	if app == nil || app.ID != expected.ID || app.IdentityHash != expected.IdentityHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if app.Result != nil {
		if *app.Result != result {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_description_applications SET result=$3,applied_at=clock_timestamp()
			WHERE entry_id=$1 AND round_number=$2 AND result IS NULL`, id, d.Input.Round, raw)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return nil, store.ErrSkillHubPublicationConflict
		}
		app, err = readPublicationDescriptionApplication(ctx, tx, t, d)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}
