package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var publicationDescriptionReleasePermitID = regexp.MustCompile(`^pubdescpermit_[a-f0-9]{32}$`)

func ValidSkillHubPublicationDescriptionApplicationID(id string) bool {
	return strings.HasPrefix(id, "pubdesc_") && len(id) == len("pubdesc_")+64 &&
		publicationSHA256.MatchString(strings.TrimPrefix(id, "pubdesc_"))
}

// A separate human-confirmed intent for the exact derived candidate target.
// It is durable evidence only: it is not a commit permit and cannot publish.
type SkillHubPublicationDescriptionReleaseIntent struct {
	EntryID                string
	ActorUID               int64
	ApplicationID          string
	AcceptanceIdentityHash string
	Target                 SkillHubPublicationCommitTarget
	IdentityHash           string
	RequestedAt            time.Time
}

// A separately issued, exact authorization for Cloud to execute an already
// confirmed derived target. It cannot authorize the legacy receipt target.
type SkillHubPublicationDescriptionReleasePermit struct {
	Intent       SkillHubPublicationDescriptionReleaseIntent
	ID           string
	AuthorizedAt time.Time
}

func (p SkillHubPublicationDescriptionReleasePermit) Validate() error {
	if h, err := p.Intent.Digest(); err != nil || p.Intent.IdentityHash != h ||
		!publicationDescriptionReleasePermitID.MatchString(p.ID) || p.AuthorizedAt.IsZero() ||
		p.AuthorizedAt.Before(p.Intent.RequestedAt) {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

func (i SkillHubPublicationDescriptionReleaseIntent) ValidateFor(c SkillHubPublicationDescriptionContinuation) error {
	if c.Validate() != nil || c.Application.Result == nil || i.ActorUID <= 0 || i.EntryID != c.Draft.EntryID ||
		i.ApplicationID != c.Application.ID || i.AcceptanceIdentityHash != c.Acceptance.IdentityHash ||
		i.Target.Validate() != nil || i.Target.CandidateHash != c.Application.Result.Derivation.DerivedCandidateHash ||
		i.RequestedAt.IsZero() || i.RequestedAt.Before(c.Acceptance.AcceptedAt) {
		return ErrSkillHubPublicationConflict
	}
	h, err := i.Digest()
	if err != nil || i.IdentityHash != h {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

func (i SkillHubPublicationDescriptionReleaseIntent) Digest() (string, error) {
	if i.ActorUID <= 0 || !ValidSkillHubPublicationDeliveryEntryID(i.EntryID) || !ValidSkillHubPublicationDescriptionApplicationID(i.ApplicationID) ||
		!publicationSHA256.MatchString(i.AcceptanceIdentityHash) || i.Target.Validate() != nil {
		return "", ErrSkillHubPublicationInvalid
	}
	type facts struct {
		EntryID                string
		ActorUID               int64
		ApplicationID          string
		AcceptanceIdentityHash string
		Target                 SkillHubPublicationCommitTarget
	}
	b, err := json.Marshal(facts{i.EntryID, i.ActorUID, i.ApplicationID, i.AcceptanceIdentityHash, i.Target})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte("catsco-publication-description-release-intent-v1\x00"), b...))
	return hex.EncodeToString(h[:]), nil
}

// This operation is invoked only after a separately authenticated human
// confirms the exact derived target. The expected continuation and Cloud plan
// are server-observed facts; callers must never decode them from a request.
type SkillHubPublicationDescriptionReleaseIntentStore interface {
	RecordSkillHubPublicationDescriptionReleaseIntent(context.Context, int64, SkillHubPublicationDescriptionContinuation, SkillHubPublicationCommitTarget) (*SkillHubPublicationDescriptionReleaseIntent, error)
	GetSkillHubPublicationDescriptionReleaseIntent(context.Context, int64, string) (*SkillHubPublicationDescriptionReleaseIntent, error)
}

type SkillHubPublicationDescriptionReleasePermitStore interface {
	AuthorizeSkillHubPublicationDescriptionRelease(context.Context, string, string, SkillHubPublicationCommitTarget) (*SkillHubPublicationDescriptionReleasePermit, error)
}

func SameSkillHubPublicationDescriptionReleaseIntent(a, b SkillHubPublicationDescriptionReleaseIntent) bool {
	a.RequestedAt, b.RequestedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}
