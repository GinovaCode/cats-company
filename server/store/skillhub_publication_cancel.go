package store

import "context"

// Cancellation applies only to the pre-Cloud workspace orchestration stage.
// The active original publisher must still own the Bot. It never deletes an
// entry/outbox, changes immutable input, or cancels/unpublishes a Cloud task.
// Implementations serialize cancellation with delivery claims and receipts.
type SkillHubPublicationCancellationStore interface {
	CancelSkillHubPublicationEntry(context.Context, int64, string) (*SkillHubPublicationEntry, error)
}
