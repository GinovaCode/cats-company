package postgres

import (
	"context"
	"github.com/openchat/openchat/server/db/sessionfork"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var _ store.SessionForkStore = (*Adapter)(nil)

func (a *Adapter) sessionForks() sessionfork.SQLStore {
	return sessionfork.SQLStore{DB: a.db, Postgres: true}
}
func (a *Adapter) CreateSessionFork(ctx context.Context, req types.SessionForkRequest) (*types.SessionFork, error) {
	return a.sessionForks().CreateSessionFork(ctx, req)
}
func (a *Adapter) GetSessionFork(ctx context.Context, uid int64, request, topic string) (*types.SessionFork, error) {
	return a.sessionForks().GetSessionFork(ctx, uid, request, topic)
}
func (a *Adapter) GetBotSessionFork(ctx context.Context, uid int64, topic string) (*types.SessionFork, error) {
	return a.sessionForks().GetBotSessionFork(ctx, uid, topic)
}
func (a *Adapter) CompleteSessionFork(ctx context.Context, uid int64, req types.SessionForkCompletion) (*types.SessionFork, error) {
	return a.sessionForks().CompleteSessionFork(ctx, uid, req)
}
func (a *Adapter) SessionForkWritable(ctx context.Context, topic string) (bool, error) {
	return a.sessionForks().SessionForkWritable(ctx, topic)
}
