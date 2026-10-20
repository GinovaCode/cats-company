package store

import (
	"context"
	"errors"
	"github.com/openchat/openchat/server/store/types"
)

var (
	ErrSessionForkNotFound  = errors.New("session_fork_not_found")
	ErrSessionForkConflict  = errors.New("session_fork_request_conflict")
	ErrSessionForkForbidden = errors.New("session_fork_forbidden")
	ErrSessionForkSource    = errors.New("session_fork_source_unavailable")
)

type SessionForkStore interface {
	CreateSessionFork(context.Context, types.SessionForkRequest) (*types.SessionFork, error)
	GetSessionFork(context.Context, int64, string, string) (*types.SessionFork, error)
	GetBotSessionFork(context.Context, int64, string) (*types.SessionFork, error)
	CompleteSessionFork(context.Context, int64, types.SessionForkCompletion) (*types.SessionFork, error)
	SessionForkWritable(context.Context, string) (bool, error)
}
