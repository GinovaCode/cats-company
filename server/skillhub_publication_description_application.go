package server

import (
	"context"

	"github.com/openchat/openchat/server/store"
)

// Internal ports, deliberately not registered as HTTP/RPC/model tools. Runtime
// verification must read the live source using the original capture under its
// workspace fence, not just replay frozen upload bytes. DeriveAndRetain must
// rederive exact confirmed text, prove unchanged other bytes, and durably retain
// one independent candidate under app.ID with quota/retention accounting. It
// must recover the SAME copy after a lost reply, never overwrite the source.
// No paid operation belongs here: deterministic retries are not model retries.
type publicationDescriptionApplicationPorts struct {
	VerifySource    func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	DeriveAndRetain func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error)
}

// This coordinator connects exact confirmation, persistent application identity
// and result acceptance without holding a DB lock during Runtime/Cloud calls.
// Production transport/retention adapters are separate, not enabled by this
// helper. A successful result is not permission to publish or resume a model.
func applyPublicationDescription(ctx context.Context, db store.SkillHubPublicationDescriptionApplicationStore, actor int64, id string, ports publicationDescriptionApplicationPorts) (*store.SkillHubPublicationDescriptionApplication, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.VerifySource == nil || ports.DeriveAndRetain == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app, err := db.PrepareSkillHubPublicationDescriptionApplication(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if app.Result != nil {
		// Historical application result only; any later continuation needs its
		// own fresh source/authority check. Never re-run an accepted operation.
		return app, nil
	}
	if err := ports.VerifySource(ctx, *app); err != nil {
		return nil, err
	}
	current, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ID != app.ID || current.IdentityHash != app.IdentityHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if current.Result != nil {
		return current, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := ports.DeriveAndRetain(ctx, *app)
	if err != nil {
		return nil, err
	}
	if err := result.ValidateFor(*app); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ports.VerifySource(ctx, *app); err != nil {
		return nil, err
	}
	return db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *app, result)
}
