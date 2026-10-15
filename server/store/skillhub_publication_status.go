package store

import (
	"context"
	"time"
)

// Historical delivery observation, not live runtime authority or permission to
// retry. Keep lease credentials, routing identities and source references out.
// Do not serialize this internal record directly into an API response.
type SkillHubPublicationSourceStatus struct {
	EntryID          string
	ActorUID         int64
	State            string
	Attempts         int
	LastCode         string
	NextAttemptAt    time.Time
	LeaseUntil       *time.Time
	SourceVerifiedAt *time.Time
	ObservedAt       time.Time
}

// Reads only for the original active human publisher, including after Bot
// ownership transfer. Reading does not claim, settle or reset any delivery.
type SkillHubPublicationSourceStatusReader interface {
	GetSkillHubPublicationSourceStatus(context.Context, int64, string) (*SkillHubPublicationSourceStatus, error)
}
