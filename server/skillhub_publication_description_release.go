package server

import (
	"context"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

// publicationDescriptionReleasePorts are trusted server-side adapters. They
// deliberately keep source verification, Cloud planning and Cloud permit
// authorization separate so a caller cannot turn a description confirmation
// into an implicit publication.
type publicationDescriptionReleasePorts struct {
	publicationDescriptionContinuationPorts
	PlanRetainedCopy func(context.Context, store.SkillHubPublicationDescriptionApplication, bool) (*publicationDescriptionReleasePlan, error)
	AuthorizeCloud   func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error)
}

type publicationDescriptionReleaseStore interface {
	publicationDescriptionContinuationStore
	publicationDescriptionReleaseAuthorizeStore
}

var _ publicationDescriptionReleaseStore = (*postgres.Adapter)(nil)

// authorizeConfirmedPublicationDescriptionReleaseFlow is the private handoff
// coordinator for a separately confirmed derived release. The caller must
// have already durably recorded an explicit human release decision through
// RecordSkillHubPublicationDescriptionReleaseIntent; confirmation of the
// description draft alone is not sufficient. This remains an internal,
// default-off adapter: it does not register a route, worker, model tool or
// legacy commit path.
func authorizeConfirmedPublicationDescriptionReleaseFlow(ctx context.Context, db publicationDescriptionReleaseStore,
	actor int64, id string, ports publicationDescriptionReleasePorts,
) (*publicationDescriptionAuthorizedRelease, error) {
	if db == nil || ports.PlanRetainedCopy == nil || ports.AuthorizeCloud == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	continuation, err := continueConfirmedPublicationDescription(ctx, db, actor, id, ports.publicationDescriptionContinuationPorts)
	if err != nil {
		return nil, err
	}
	// Read and validate the durable human release intent before asking Cloud to
	// reserve a target. Its target is the sole source of version-policy truth;
	// no caller-supplied flag can cause a speculative Cloud reservation.
	intent, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.ActorUID != actor || intent.ValidateFor(continuation.checkpoint) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	planned, err := planConfirmedPublicationDescription(ctx, db, actor, id, continuation, intent.Target.ConfirmVersionPublish, publicationDescriptionReleasePlanPorts{
		VerifySource:     ports.VerifySource,
		PlanRetainedCopy: ports.PlanRetainedCopy,
	})
	if err != nil {
		return nil, err
	}
	return authorizeConfirmedPublicationDescriptionRelease(ctx, db, actor, id, planned, publicationDescriptionReleaseAuthorizePorts{
		VerifySource:   ports.VerifySource,
		AuthorizeCloud: ports.AuthorizeCloud,
	})
}
