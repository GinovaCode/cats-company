package postgres

import (
	"context"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionContinuationStore = (*Adapter)(nil)

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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}
