package store

import (
	"context"
	"time"
)

// Internal cancellation intent, not a Cloud cancellation confirmation. States:
// local_only: no upload authority/receipt exists in the retained ledger;
// waiting_receipt: upload authority existed, but acceptance is not yet observed;
// ready: the exact historical receipt is available for FUTURE peer delivery.
// None of these states confirms Cloud cancellation or current execution state.
// No HTTP/RPC/worker caller is registered for this foundation.
type SkillHubPublicationCancellationIntent struct {
	ID          string
	EntryID     string
	State       string
	RequestedAt time.Time
}

type SkillHubPublicationCancellationIntentStore interface {
	// Actor must be the active original human and still own the Bot. A future
	// adapter must derive actor from authenticated human context, not tool args.
	RequestSkillHubPublicationCancellation(context.Context, int64, string) (*SkillHubPublicationCancellationIntent, error)
	// Historical read: active original human only, even after Bot transfer.
	// Read does not send, retry or renew anything and exposes no peer credentials.
	GetSkillHubPublicationCancellationIntent(context.Context, int64, string) (*SkillHubPublicationCancellationIntent, error)
}
