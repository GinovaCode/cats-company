package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionReleasePlanPorts struct {
	VerifySource     func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	PlanRetainedCopy func(context.Context, store.SkillHubPublicationDescriptionApplication, bool) (*publicationDescriptionReleasePlan, error)
}

// Private point-in-time handoff, not a commit intent/permit or public status.
// No bytes or confirmation flag from a browser/model can construct the trusted
// continuation. Durable candidate-bound admission remains a separate step.
type publicationDescriptionPlannedRelease struct {
	checkpoint store.SkillHubPublicationDescriptionContinuation
	plan       publicationDescriptionReleasePlan
}

func planConfirmedPublicationDescription(ctx context.Context, db store.SkillHubPublicationDescriptionContinuationStore, actor int64, id string, handoff *publicationDescriptionContinuation, confirmVersion bool, ports publicationDescriptionReleasePlanPorts) (*publicationDescriptionPlannedRelease, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || handoff == nil || ports.VerifySource == nil || ports.PlanRetainedCopy == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	baseline := snapshotDescriptionContinuation(handoff.checkpoint)
	app := baseline.Application
	if baseline.Validate() != nil || app.Input.Receipt.Query.EntryID != id || validatePublicationDescriptionCopyApplication(app) != nil ||
		int64(len(handoff.bytes)) != app.Result.RequestBytes || publicationDescriptionSHA256Hex(string(handoff.bytes)) != app.Result.Derivation.DerivedRequestHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
		if err != nil {
			return err
		}
		if current == nil || current.Validate() != nil || !reflect.DeepEqual(*current, baseline) {
			return store.ErrSkillHubPublicationConflict
		}
		return ctx.Err()
	}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(app))
	}
	if err := check(); err != nil {
		return nil, err
	}
	if err := verify(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	plan, err := ports.PlanRetainedCopy(ctx, snapshotPublicationDescriptionApplication(app), confirmVersion)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.validateFor(app, confirmVersion) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	frozen := *plan // All plan fields are values; never retain a peer-owned pointer.
	if err := verify(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return &publicationDescriptionPlannedRelease{checkpoint: baseline, plan: frozen}, nil
}
