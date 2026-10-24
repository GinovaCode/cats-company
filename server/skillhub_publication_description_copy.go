package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

// A read-only authority checkpoint interface: this lane cannot prepare a new
// application, complete one, or allocate/reset a repair round.
type publicationDescriptionCopyStore interface {
	GetSkillHubPublicationDescriptionApplication(context.Context, int64, string) (*store.SkillHubPublicationDescriptionApplication, error)
}

type publicationDescriptionCopyPorts struct {
	VerifySource     func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	ReadRetainedCopy func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error)
}

// Internal, deliberately not serializable through a public task/status API.
// It is a point-in-time verified copy, never an execution/publication grant.
type publicationDescriptionCopy struct {
	application store.SkillHubPublicationDescriptionApplication
	bytes       []byte
}

func snapshotPublicationDescriptionApplication(app store.SkillHubPublicationDescriptionApplication) store.SkillHubPublicationDescriptionApplication {
	if p := app.Input.Draft.Proposal.EntryIndex; p != nil {
		value := *p
		app.Input.Draft.Proposal.EntryIndex = &value
	}
	if app.Result != nil {
		value := *app.Result
		app.Result = &value
	}
	if app.AppliedAt != nil {
		value := *app.AppliedAt
		app.AppliedAt = &value
	}
	return app
}

func validatePublicationDescriptionCopyApplication(app store.SkillHubPublicationDescriptionApplication) error {
	identity, err := app.Input.IdentityHash()
	if err != nil || app.IdentityHash != identity || app.ID != "pubdesc_"+identity || app.CreatedAt.IsZero() ||
		app.Result == nil || app.AppliedAt == nil || app.AppliedAt.IsZero() || app.AppliedAt.Before(app.CreatedAt) {
		return store.ErrSkillHubPublicationConflict
	}
	if err := app.Result.ValidateFor(app); err != nil {
		return err
	}
	if app.Result.Derivation.ProposalDigest != publicationDescriptionProposalDigest(app.Input.Draft.Proposal) {
		return store.ErrSkillHubPublicationConflict
	}
	return nil
}

// readConfirmedPublicationDescription brackets private Cloud recovery with fresh
// CatsCo current-authority and live Runtime source checks. It has no production
// route, RPC, worker or model tool. A later continuation must check again at its
// own state transition; these short reads do not lock authority across a network.
func readConfirmedPublicationDescription(ctx context.Context, db publicationDescriptionCopyStore, actor int64, id string, ports publicationDescriptionCopyPorts) (*publicationDescriptionCopy, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.VerifySource == nil || ports.ReadRetainedCopy == nil {
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
	checkpoint := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		live, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
		if err != nil {
			return err
		}
		if live == nil || validatePublicationDescriptionCopyApplication(*live) != nil || !reflect.DeepEqual(*live, app) {
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
	if err := verify(); err != nil {
		return nil, err
	}
	if err := checkpoint(); err != nil {
		return nil, err
	}
	raw, err := ports.ReadRetainedCopy(ctx, snapshotPublicationDescriptionApplication(app))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) != app.Result.RequestBytes || publicationDescriptionSHA256Hex(string(raw)) != app.Result.Derivation.DerivedRequestHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	// Own these bytes before any further external call can reuse its buffer.
	raw = append([]byte(nil), raw...)
	if err := verify(); err != nil {
		return nil, err
	}
	if err := checkpoint(); err != nil {
		return nil, err
	}
	return &publicationDescriptionCopy{application: app, bytes: raw}, nil
}
