package store

import (
	"context"
	"time"
)

// CatsCo's local observation of historical Cloud acceptance, NOT live Cloud
// execution state. No receipt does not prove that upload never happened.
// This internal record must only be exposed through an explicit projection.
type SkillHubPublicationCloudStatus struct {
	EntryID           string
	ActorUID          int64
	LocalIntentState  string
	ReceiptRecordedAt *time.Time
	ObservedAt        time.Time
}

// Original active human publisher only, including after Bot ownership transfer.
// Reading cannot deliver, retry, cancel, recreate or execute a Cloud task.
type SkillHubPublicationCloudStatusReader interface {
	GetSkillHubPublicationCloudStatus(context.Context, int64, string) (*SkillHubPublicationCloudStatus, error)
}
