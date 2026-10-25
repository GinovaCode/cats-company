package server

import (
	"context"
	"errors"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionAcceptanceStore interface {
	publicationDescriptionCopyStore
	store.SkillHubPublicationDescriptionAcceptanceStore
}

// Internal retained checkpoint, deliberately not a public status response or an
// execution/commit grant. A later continuation needs its own authority fence.
type publicationDescriptionAcceptance struct {
	application store.SkillHubPublicationDescriptionApplication
	accepted    store.SkillHubPublicationDescriptionAcceptance
}

func snapshotDescriptionAcceptance(a store.SkillHubPublicationDescriptionAcceptance) store.SkillHubPublicationDescriptionAcceptance {
	a.Report = snapshotDescriptionValidationReport(a.Report)
	return a
}

// No route, RPC, worker, executor or model registration. A retry recovers the
// first accepted report rather than fetching a fresh report UUID and replacing
// history. Concurrent different valid reports are first-writer-wins; a loser
// recovers the winner only after fresh current authority AND source checks.
func acceptConfirmedPublicationDescription(ctx context.Context, db publicationDescriptionAcceptanceStore, actor int64, id string, ports publicationDescriptionValidationPorts) (*publicationDescriptionAcceptance, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.VerifySource == nil || ports.ReadValidationReport == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Input.Receipt.Query.EntryID != id {
		return nil, store.ErrSkillHubPublicationConflict
	}
	app := snapshotPublicationDescriptionApplication(*current)
	if err := validatePublicationDescriptionCopyApplication(app); err != nil {
		return nil, err
	}

	recoverAccepted := func() (*publicationDescriptionAcceptance, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		accepted, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, snapshotPublicationDescriptionApplication(app))
		if err != nil || accepted == nil {
			return nil, err
		}
		retained := snapshotDescriptionAcceptance(*accepted)
		if err := retained.ValidateFor(app); err != nil {
			return nil, err
		}
		if err := ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(app)); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		live, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		if live == nil || !reflect.DeepEqual(*live, app) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		final, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, snapshotPublicationDescriptionApplication(app))
		if err != nil {
			return nil, err
		}
		if final == nil || !reflect.DeepEqual(*final, retained) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &publicationDescriptionAcceptance{application: app, accepted: retained}, nil
	}
	if got, err := recoverAccepted(); err != nil || got != nil {
		return got, err
	}
	validated, err := validateConfirmedPublicationDescription(ctx, db, actor, id, ports)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(validated.application, app) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err = db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, snapshotPublicationDescriptionApplication(app), snapshotDescriptionValidationReport(validated.report))
	if err != nil && !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		return nil, err
	}
	// Also fence AFTER persistence; source checks cannot be atomic with a remote
	// runtime. An acceptance may remain historical if authority/source changes.
	got, recoveryErr := recoverAccepted()
	if recoveryErr != nil {
		return nil, recoveryErr
	}
	if got == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return got, nil
}
