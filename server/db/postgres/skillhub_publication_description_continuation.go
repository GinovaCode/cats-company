package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionContinuationStore = (*Adapter)(nil)
var _ store.SkillHubPublicationDescriptionReleaseIntentStore = (*Adapter)(nil)
var _ store.SkillHubPublicationDescriptionReleasePermitStore = (*Adapter)(nil)

func readSkillHubPublicationDescriptionContinuation(ctx context.Context, tx *sql.Tx, actor int64, id string) (*store.SkillHubPublicationDescriptionContinuation, error) {
	t, d, err := publicationDescriptionApplicationContext(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	app, err := readPublicationDescriptionApplication(ctx, tx, t, d)
	if err != nil {
		return nil, err
	}
	if app == nil || app.Result == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	accepted, err := readDescriptionAcceptance(ctx, tx, *app)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	c := &store.SkillHubPublicationDescriptionContinuation{Task: *t, Draft: *d, Application: *app, Acceptance: *accepted}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (a *Adapter) GetSkillHubPublicationDescriptionContinuation(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionContinuation, error) {
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
	c, err := readSkillHubPublicationDescriptionContinuation(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

func readSkillHubPublicationDescriptionReleaseIntent(ctx context.Context, tx *sql.Tx, c store.SkillHubPublicationDescriptionContinuation) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	i := &store.SkillHubPublicationDescriptionReleaseIntent{EntryID: c.Draft.EntryID, ApplicationID: c.Application.ID}
	var target []byte
	err := tx.QueryRowContext(ctx, `SELECT actor_uid,acceptance_identity_hash,target,identity_hash,requested_at
		FROM skillhub_publication_description_release_intents WHERE application_id=$1`, c.Application.ID).
		Scan(&i.ActorUID, &i.AcceptanceIdentityHash, &target, &i.IdentityHash, &i.RequestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(target, &i.Target) != nil || i.ValidateFor(c) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return i, nil
}

func (a *Adapter) descriptionReleaseIntent(ctx context.Context, actor int64, entryID string, expected *store.SkillHubPublicationDescriptionContinuation, target *store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(entryID) || (expected == nil) != (target == nil) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := readSkillHubPublicationDescriptionContinuation(ctx, tx, actor, entryID)
	if err != nil {
		return nil, err
	}
	if expected != nil && (!reflect.DeepEqual(current, expected) || target.Validate() != nil || current.Application.Result == nil ||
		target.CandidateHash != current.Application.Result.Derivation.DerivedCandidateHash) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	intent, err := readSkillHubPublicationDescriptionReleaseIntent(ctx, tx, *current)
	if err != nil {
		return nil, err
	}
	if expected != nil {
		candidate := store.SkillHubPublicationDescriptionReleaseIntent{EntryID: entryID, ActorUID: actor, ApplicationID: current.Application.ID,
			AcceptanceIdentityHash: current.Acceptance.IdentityHash, Target: *target, RequestedAt: current.Acceptance.AcceptedAt}
		candidate.IdentityHash, err = candidate.Digest()
		if err != nil || candidate.ValidateFor(*current) != nil {
			return nil, store.ErrSkillHubPublicationInvalid
		}
		if intent != nil {
			if !store.SameSkillHubPublicationDescriptionReleaseIntent(*intent, candidate) {
				return nil, store.ErrSkillHubPublicationConflict
			}
		} else {
			raw, err := json.Marshal(candidate.Target)
			if err != nil {
				return nil, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_release_intents
				(application_id,entry_id,actor_uid,acceptance_identity_hash,target,identity_hash)
				VALUES($1,$2,$3,$4,$5,$6)`, candidate.ApplicationID, candidate.EntryID, candidate.ActorUID,
				candidate.AcceptanceIdentityHash, raw, candidate.IdentityHash)
			if err != nil {
				return nil, err
			}
			intent, err = readSkillHubPublicationDescriptionReleaseIntent(ctx, tx, *current)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return intent, nil
}

func (a *Adapter) RecordSkillHubPublicationDescriptionReleaseIntent(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionContinuation, target store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	return a.descriptionReleaseIntent(ctx, actor, expected.Draft.EntryID, &expected, &target)
}

func (a *Adapter) GetSkillHubPublicationDescriptionReleaseIntent(ctx context.Context, actor int64, entryID string) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	return a.descriptionReleaseIntent(ctx, actor, entryID, nil, nil)
}

func (a *Adapter) AuthorizeSkillHubPublicationDescriptionRelease(ctx context.Context, entryID, applicationID string, target store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleasePermit, error) {
	if !store.ValidSkillHubPublicationDeliveryEntryID(entryID) || !store.ValidSkillHubPublicationDescriptionApplicationID(applicationID) || target.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var actor int64
	if err := tx.QueryRowContext(ctx, `SELECT actor_uid FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, entryID).Scan(&actor); errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	} else if err != nil {
		return nil, err
	}
	intent := &store.SkillHubPublicationDescriptionReleaseIntent{EntryID: entryID, ApplicationID: applicationID}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT actor_uid,acceptance_identity_hash,target,identity_hash,requested_at
		FROM skillhub_publication_description_release_intents WHERE application_id=$1 AND entry_id=$2`, applicationID, entryID).
		Scan(&intent.ActorUID, &intent.AcceptanceIdentityHash, &raw, &intent.IdentityHash, &intent.RequestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &intent.Target) != nil || intent.ActorUID != actor || intent.Target != target {
		return nil, store.ErrSkillHubPublicationConflict
	}
	hash, hashErr := intent.Digest()
	if hashErr != nil || intent.IdentityHash != hash || intent.RequestedAt.IsZero() {
		return nil, store.ErrSkillHubPublicationConflict
	}
	permit := &store.SkillHubPublicationDescriptionReleasePermit{Intent: *intent}
	var savedEntry, savedHash string
	err = tx.QueryRowContext(ctx, `SELECT permit_id,entry_id,intent_identity_hash,authorized_at
		FROM skillhub_publication_description_release_permits WHERE application_id=$1`, applicationID).
		Scan(&permit.ID, &savedEntry, &savedHash, &permit.AuthorizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Existing permits are immutable historical authorizations and may be
		// replayed after local authority changes. Only new issuance rechecks it.
		current, err := readSkillHubPublicationDescriptionContinuation(ctx, tx, actor, entryID)
		if err != nil {
			return nil, err
		}
		if intent.ValidateFor(*current) != nil {
			return nil, fmt.Errorf("derived release intent no longer matches its accepted continuation: %w", store.ErrSkillHubPublicationConflict)
		}
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, err
		}
		permit.ID = "pubdescpermit_" + suffix
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_release_permits(application_id,entry_id,permit_id,intent_identity_hash) VALUES($1,$2,$3,$4)`, applicationID, entryID, permit.ID, intent.IdentityHash)
		if err != nil {
			return nil, err
		}
		err = tx.QueryRowContext(ctx, `SELECT entry_id,intent_identity_hash,authorized_at FROM skillhub_publication_description_release_permits WHERE application_id=$1`, applicationID).
			Scan(&savedEntry, &savedHash, &permit.AuthorizedAt)
	}
	if err != nil {
		return nil, err
	}
	if savedEntry != entryID || savedHash != intent.IdentityHash {
		return nil, fmt.Errorf("derived release permit does not match its retained intent: %w", store.ErrSkillHubPublicationConflict)
	}
	if err := permit.Validate(); err != nil {
		return nil, fmt.Errorf("invalid derived release permit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return permit, nil
}
