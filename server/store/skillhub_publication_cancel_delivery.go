package store

import (
	"context"
	"regexp"
	"time"
)

var publicationCancellationID = regexp.MustCompile(`^pubcancel_[a-f0-9]{32}$`)

type SkillHubPublicationCancellationScope struct{ Issuer, Audience string }

func (s SkillHubPublicationCancellationScope) Validate() error {
	if !ValidSkillHubPublicationOrigin(s.Issuer) || !ValidSkillHubPublicationOrigin(s.Audience) || s.Issuer == s.Audience {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// Immutable intent/receipt plus a fenced scheduling lease. Never a new human
// action: delivery can finish after transfer, disablement or runtime disconnect.
type SkillHubPublicationCancellationLease struct {
	CancellationID string
	Receipt        SkillHubPublicationCloudReceipt
	Token          string
	Attempt        int
	ExpiresAt      time.Time
}

func (l SkillHubPublicationCancellationLease) Validate() error {
	if !publicationCancellationID.MatchString(l.CancellationID) || l.Receipt.Validate() != nil ||
		!ValidSkillHubPublicationDeliveryToken(l.Token) || l.Attempt < 1 || l.Attempt > 8 || l.ExpiresAt.IsZero() {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationCancellationOutcome struct {
	Kind                    string // confirmed, retry, conflict, not_found; latter two require investigation
	CancelledAt, ObservedAt time.Time
}

func (o SkillHubPublicationCancellationOutcome) Validate() error {
	switch o.Kind {
	case "confirmed":
		if o.CancelledAt.IsZero() || o.ObservedAt.IsZero() || o.CancelledAt.After(o.ObservedAt) {
			return ErrSkillHubPublicationInvalid
		}
	case "retry", "conflict", "not_found":
		if !o.CancelledAt.IsZero() || !o.ObservedAt.IsZero() {
			return ErrSkillHubPublicationInvalid
		}
	default:
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// Private historical projection; excludes lease tokens and Cloud routing data.
type SkillHubPublicationCancellationDelivery struct {
	CancellationID          string
	State                   string // pending, leased, confirmed, needs_attention, exhausted
	Attempts                int
	LastCode                string
	CancelledAt, ObservedAt *time.Time
}

type SkillHubPublicationCancellationDeliveryStore interface {
	ClaimSkillHubPublicationCancellation(context.Context, SkillHubPublicationCancellationScope) (*SkillHubPublicationCancellationLease, error)
	FinishSkillHubPublicationCancellation(context.Context, SkillHubPublicationCancellationLease, SkillHubPublicationCancellationOutcome) error
	GetSkillHubPublicationCancellationDelivery(context.Context, int64, string) (*SkillHubPublicationCancellationDelivery, error)
}
