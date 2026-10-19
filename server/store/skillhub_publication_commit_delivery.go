package store

import (
	"context"
	"regexp"
	"time"
)

type SkillHubPublicationCommitLease struct {
	Intent    SkillHubPublicationCommitIntent
	Token     string
	Attempt   int
	ExpiresAt time.Time
}

func (l SkillHubPublicationCommitLease) Validate() error {
	h, e := l.Intent.Input.IdentityHash()
	if e != nil || h != l.Intent.IdentityHash || l.Intent.RequestedAt.IsZero() || !ValidSkillHubPublicationDeliveryToken(l.Token) || l.Attempt < 1 || l.Attempt > 8 || l.ExpiresAt.IsZero() {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationCommitOutcome struct{ Kind, PermitID string }

func (o SkillHubPublicationCommitOutcome) Validate() error {
	if o.Kind == "accepted" && regexp.MustCompile(`^pubpermit_[a-f0-9]{32}$`).MatchString(o.PermitID) {
		return nil
	}
	if o.PermitID == "" {
		switch o.Kind {
		case "retry", "cancelled", "denied", "conflict", "not_found":
			return nil
		}
	}
	return ErrSkillHubPublicationInvalid
}

type SkillHubPublicationCommitDelivery struct {
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
	LastCode string `json:"lastCode"`
}

// Delivery only: never manufactures human intent, a plan or a permit. Eight
// transport attempts are separate from Cloud's publish budget and repair rounds.
type SkillHubPublicationCommitDeliveryStore interface {
	ClaimSkillHubPublicationCommitDelivery(context.Context, SkillHubPublicationCancellationScope) (*SkillHubPublicationCommitLease, error)
	FinishSkillHubPublicationCommitDelivery(context.Context, SkillHubPublicationCommitLease, SkillHubPublicationCommitOutcome) error
	GetSkillHubPublicationCommitDelivery(context.Context, int64, string) (*SkillHubPublicationCommitDelivery, error)
}
