package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionContinuationStore interface {
	store.SkillHubPublicationDescriptionApplicationStore
	store.SkillHubPublicationDescriptionAcceptanceStore
	store.SkillHubPublicationDescriptionContinuationStore
}

type publicationDescriptionContinuationPorts struct {
	VerifySource         func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	DeriveAndRetain      func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error)
	ReadValidationReport func(context.Context, store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error)
	ReadRetainedCopy     func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error)
}

// Private handoff for a future release adapter. Neither the checkpoint nor the
// private bytes are a browser status or permission to publish. The consumer
// must still recheck source/authority and obtain a candidate-bound commit permit.
type publicationDescriptionContinuation struct {
	checkpoint store.SkillHubPublicationDescriptionContinuation
	bytes      []byte
}

func snapshotDescriptionContinuation(c store.SkillHubPublicationDescriptionContinuation) store.SkillHubPublicationDescriptionContinuation {
	c.Application = snapshotPublicationDescriptionApplication(c.Application)
	c.Acceptance = snapshotDescriptionAcceptance(c.Acceptance)
	// Keep even equivalent draft/application pointer fields independently owned.
	if p := c.Draft.Input.Proposal.EntryIndex; p != nil {
		v := *p
		c.Draft.Input.Proposal.EntryIndex = &v
	}
	if p := c.Draft.ConfirmedAt; p != nil {
		v := *p
		c.Draft.ConfirmedAt = &v
	}
	c.Task.Rounds = append([]store.SkillHubPublicationRepairRound(nil), c.Task.Rounds...)
	for i := range c.Task.Rounds {
		if p := c.Task.Rounds[i].Result; p != nil {
			v := *p
			c.Task.Rounds[i].Result = &v
		}
	}
	return c
}

// Connect exact confirmation -> deterministic private application -> durable
// passing report -> verified retained bytes. Lost replies resume the same
// application/acceptance, never another round or a paid-model operation. All
// ports are trusted internal adapters; no production handler/worker is wired.
func continueConfirmedPublicationDescription(ctx context.Context, db publicationDescriptionContinuationStore, actor int64, id string, ports publicationDescriptionContinuationPorts) (*publicationDescriptionContinuation, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.VerifySource == nil ||
		ports.DeriveAndRetain == nil || ports.ReadValidationReport == nil || ports.ReadRetainedCopy == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	app, err := applyPublicationDescription(ctx, db, actor, id, publicationDescriptionApplicationPorts{
		VerifySource: func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) error {
			return ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(app))
		},
		DeriveAndRetain: func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
			return ports.DeriveAndRetain(ctx, snapshotPublicationDescriptionApplication(app))
		},
	})
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	baseline := snapshotPublicationDescriptionApplication(*app)
	if baseline.Input.Receipt.Query.EntryID != id || validatePublicationDescriptionCopyApplication(baseline) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	accepted, err := acceptConfirmedPublicationDescription(ctx, db, actor, id, publicationDescriptionValidationPorts{
		VerifySource: ports.VerifySource, ReadValidationReport: ports.ReadValidationReport,
	})
	if err != nil {
		return nil, err
	}
	if accepted == nil || !reflect.DeepEqual(accepted.application, baseline) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	expectedAcceptance := snapshotDescriptionAcceptance(accepted.accepted)
	read := func() (*store.SkillHubPublicationDescriptionContinuation, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Validate() != nil || !reflect.DeepEqual(current.Application, baseline) ||
			!reflect.DeepEqual(current.Acceptance, expectedAcceptance) {
			return nil, store.ErrSkillHubPublicationConflict
		}
		copy := snapshotDescriptionContinuation(*current)
		return &copy, ctx.Err()
	}
	checkpoint, err := read()
	if err != nil {
		return nil, err
	}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(baseline))
	}
	check := func() error {
		live, err := read()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(*live, *checkpoint) {
			return store.ErrSkillHubPublicationConflict
		}
		return nil
	}
	if err := verify(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	raw, err := ports.ReadRetainedCopy(ctx, snapshotPublicationDescriptionApplication(baseline))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) != baseline.Result.RequestBytes || publicationDescriptionSHA256Hex(string(raw)) != baseline.Result.Derivation.DerivedRequestHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	raw = append([]byte(nil), raw...)
	if err := verify(); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return &publicationDescriptionContinuation{checkpoint: *checkpoint, bytes: raw}, nil
}
