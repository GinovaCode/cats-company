package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionReleaseAuthorizeStore interface {
	store.SkillHubPublicationDescriptionContinuationStore
	GetSkillHubPublicationDescriptionReleaseIntent(context.Context, int64, string) (*store.SkillHubPublicationDescriptionReleaseIntent, error)
}

type publicationDescriptionReleaseAuthorizePorts struct {
	VerifySource   func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	AuthorizeCloud func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error)
}

// This internal handoff proves current human intent and a Cloud-retained permit.
// It is not a job, publication commit, or permission to skip worker checks.
type publicationDescriptionAuthorizedRelease struct {
	checkpoint store.SkillHubPublicationDescriptionContinuation
	intent     store.SkillHubPublicationDescriptionReleaseIntent
	plan       publicationDescriptionReleasePlan
	permit     publicationDescriptionCloudReleasePermit
}

func authorizeConfirmedPublicationDescriptionRelease(ctx context.Context, db publicationDescriptionReleaseAuthorizeStore,
	actor int64, id string, handoff *publicationDescriptionPlannedRelease, ports publicationDescriptionReleaseAuthorizePorts,
) (*publicationDescriptionAuthorizedRelease, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || handoff == nil ||
		ports.VerifySource == nil || ports.AuthorizeCloud == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	baseline := snapshotDescriptionContinuation(handoff.checkpoint)
	app := baseline.Application
	plan := handoff.plan
	if baseline.Validate() != nil || app.Input.Receipt.Query.EntryID != id ||
		plan.validateFor(app, plan.Target.ConfirmVersionPublish) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	intent, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.ActorUID != actor || intent.ValidateFor(baseline) != nil || intent.Target != plan.Target {
		return nil, store.ErrSkillHubPublicationConflict
	}
	expected := *intent
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
		if err != nil {
			return err
		}
		currentIntent, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, id)
		if err != nil {
			return err
		}
		if current == nil || current.Validate() != nil || !reflect.DeepEqual(*current, baseline) ||
			currentIntent == nil || !reflect.DeepEqual(*currentIntent, expected) {
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
	permit, err := ports.AuthorizeCloud(ctx, snapshotPublicationDescriptionApplication(app), expected)
	if err != nil {
		return nil, err
	}
	if permit == nil || permit.validateFor(app, expected) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	frozen := *permit
	replayed := *permit.Replayed
	frozen.Replayed = &replayed
	if err := verify(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return &publicationDescriptionAuthorizedRelease{checkpoint: baseline, intent: expected, plan: plan, permit: frozen}, nil
}
