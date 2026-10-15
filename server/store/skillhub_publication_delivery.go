package store

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrSkillHubPublicationBusy      = errors.New("publication delivery is not due")
	ErrSkillHubPublicationStale     = errors.New("publication delivery lease is stale")
	ErrSkillHubPublicationSettled   = errors.New("publication source verification is settled")
	ErrSkillHubPublicationExhausted = errors.New("publication delivery attempts exhausted")
)

const SkillHubPublicationDeliveryAttempts = 8
const SkillHubPublicationDeliveryLease = 30 * time.Second

var publicationEntryIDPattern = regexp.MustCompile(`^pubentry_[a-f0-9]{32}$`)
var publicationLeasePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// This internal observation MUST be derived from an authenticated Bot socket,
// its current body lease and registered device on the node doing the work. It
// is not a browser payload, upload grant or proof of local file accessibility.
// A reconnect may advance RouteGeneration but cannot change any source selector.
type SkillHubPublicationDeliveryBinding struct {
	ActorUID     int64
	BotUID       int64
	Source       SkillHubPublicationSource
	NodeID       string
	ConnectionID string
}

func (b SkillHubPublicationDeliveryBinding) Validate() error {
	in := SkillHubPublicationEntryInput{ActorUID: b.ActorUID, BotUID: b.BotUID,
		Entrypoint: "workspace", RequestID: "delivery-validation", Source: b.Source}
	if in.Validate() != nil || !publicationReference.MatchString(b.NodeID) || !publicationReference.MatchString(b.ConnectionID) {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationDeliveryLeaseRecord struct {
	EntryID   string
	Token     string
	Binding   SkillHubPublicationDeliveryBinding
	Attempt   int
	ExpiresAt time.Time
}

// Retry backoff is persisted, not reset by another node or process restart.
func SkillHubPublicationDeliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		return 5 * time.Minute
	}
	d := 5 * time.Second * time.Duration(1<<(attempt-1))
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}

func ValidSkillHubPublicationDeliveryEntryID(id string) bool {
	return publicationEntryIDPattern.MatchString(id)
}
func ValidSkillHubPublicationDeliveryToken(token string) bool {
	return publicationLeasePattern.MatchString(token)
}

// Only these transient failures may retry automatically. Source/identity/security
// problems settle for user action; none of these codes starts model repair.
func SkillHubPublicationDeliveryOutcome(code string) (state string, valid bool) {
	switch code {
	case "":
		return "source_verified", true
	case "publication.runtime_timeout", "publication.runtime_unavailable":
		return "pending", true
	case "publication.source_missing", "publication.source_changed", "publication.source_unsafe", "publication.runtime_unsupported":
		return "needs_user_action", true
	default:
		return "", false
	}
}

// Persistence only: no dispatcher or HTTP/RPC caller is wired in this slice.
// A lease fences DB receipts, NOT external execution. Future consumers must
// revalidate socket/lease/capability before dispatch and receipt acceptance.
// No network/file IO is allowed inside these store transactions. A verified
// source is only a read-only probe receipt, never a capture/upload/publication.
type SkillHubPublicationDeliveryStore interface {
	ListSkillHubPublicationDeliveries(context.Context, int64, int) ([]string, error)
	ClaimSkillHubPublicationDelivery(context.Context, string, SkillHubPublicationDeliveryBinding) (*SkillHubPublicationDeliveryLeaseRecord, error)
	FinishSkillHubPublicationDelivery(context.Context, SkillHubPublicationDeliveryLeaseRecord, string) error
}
