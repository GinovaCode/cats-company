package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionAcceptanceStore = (*Adapter)(nil)

func readDescriptionAcceptance(ctx context.Context, tx *sql.Tx, app store.SkillHubPublicationDescriptionApplication) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	a := &store.SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID}
	var result, report []byte
	err := tx.QueryRowContext(ctx, `SELECT result,report,identity_hash,accepted_at FROM skillhub_publication_description_acceptances WHERE application_id=$1`, app.ID).
		Scan(&result, &report, &a.IdentityHash, &a.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(result, &a.Result) != nil || json.Unmarshal(report, &a.Report) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := a.ValidateFor(app); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) descriptionAcceptance(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionApplication, report *store.PublicationValidationReport) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	id := expected.Input.Receipt.Query.EntryID
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || expected.Result == nil || expected.AppliedAt == nil {
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
	if app == nil || !reflect.DeepEqual(*app, expected) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	accepted, err := readDescriptionAcceptance(ctx, tx, *app)
	if err != nil {
		return nil, err
	}
	if report != nil {
		// Preflight uses the retained lower bound, not the application host's
		// wall clock. PostgreSQL stamps and validates the actual acceptance time.
		candidate := store.SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID, Result: *app.Result, Report: *report, AcceptedAt: *app.AppliedAt}
		candidate.IdentityHash, err = candidate.Digest()
		if err != nil {
			return nil, err
		}
		if err := candidate.ValidateFor(*app); err != nil {
			return nil, err
		}
		if accepted != nil {
			if accepted.IdentityHash != candidate.IdentityHash {
				return nil, store.ErrSkillHubPublicationConflict
			}
		} else {
			resultJSON, err := json.Marshal(candidate.Result)
			if err != nil {
				return nil, err
			}
			reportJSON, err := json.Marshal(candidate.Report)
			if err != nil {
				return nil, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_acceptances(application_id,result,report,identity_hash) VALUES($1,$2,$3,$4)`,
				app.ID, resultJSON, reportJSON, candidate.IdentityHash)
			if err != nil {
				return nil, err
			}
			accepted, err = readDescriptionAcceptance(ctx, tx, *app)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return accepted, nil
}

func (a *Adapter) GetSkillHubPublicationDescriptionAcceptance(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionApplication) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	return a.descriptionAcceptance(ctx, actor, expected, nil)
}

func (a *Adapter) AcceptSkillHubPublicationDescriptionValidation(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionApplication, report store.PublicationValidationReport) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	return a.descriptionAcceptance(ctx, actor, expected, &report)
}
