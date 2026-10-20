package store

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const SkillHubPublicationUploadAction = "candidate.upload"
const SkillHubPublicationUploadTTL = 120 * time.Second
const SkillHubPublicationUploadCooldown = 5 * time.Second
const SkillHubPublicationUploadMaxIssues = 8
const SkillHubPublicationUploadMaxBytes int64 = 75 * 1024 * 1024

var publicationSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var publicationHTTPSOrigin = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$`)
var publicationHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Only canonical, configured HTTPS origins; never caller-selected authority URLs.
func ValidSkillHubPublicationOrigin(origin string) bool {
	if len(origin) > 256 || !publicationHTTPSOrigin.MatchString(origin) {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || len(u.Hostname()) > 253 {
		return false
	}
	for _, label := range strings.Split(u.Hostname(), ".") {
		if len(label) > 63 || !publicationHostLabel.MatchString(label) {
			return false
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != u.Port() {
			return false
		}
	}
	return true
}

// Internal-only input. The future runtime adapter must derive Binding from the
// authenticated socket/body lease and obtain digests from a private immutable
// capture. The issuer now requires the matching persisted capture receipt;
// a prior source probe alone is NOT proof of that capture. Never decode
// a browser/model payload directly into this input.
type SkillHubPublicationUploadInput struct {
	EntryID            string
	Binding            SkillHubPublicationDeliveryBinding
	Issuer             string
	Audience           string
	Action             string
	Revision           int
	RequestHash        string
	SourceSnapshotHash string
	RequestBytes       int64
}

func (in SkillHubPublicationUploadInput) Validate() error {
	if !ValidSkillHubPublicationDeliveryEntryID(in.EntryID) || in.Binding.Validate() != nil ||
		!ValidSkillHubPublicationOrigin(in.Issuer) || !ValidSkillHubPublicationOrigin(in.Audience) || in.Issuer == in.Audience ||
		in.Action != SkillHubPublicationUploadAction || in.Revision != 1 ||
		!publicationSHA256.MatchString(in.RequestHash) || !publicationSHA256.MatchString(in.SourceSnapshotHash) ||
		in.RequestBytes < 1 || in.RequestBytes > SkillHubPublicationUploadMaxBytes {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// Opaque bearer material is deliberately not printable/JSON serializable.
// Reveal is only for a future dedicated authenticated response/header; never
// put it in a model prompt, generic RPC log, URL, event or durable payload.
type SkillHubPublicationUploadToken struct{ value string }

func ParseSkillHubPublicationUploadToken(raw string) (SkillHubPublicationUploadToken, error) {
	if !strings.HasPrefix(raw, "pubupload_") || !publicationSHA256.MatchString(strings.TrimPrefix(raw, "pubupload_")) {
		return SkillHubPublicationUploadToken{}, ErrSkillHubPublicationInvalid
	}
	return SkillHubPublicationUploadToken{value: raw}, nil
}
func (t SkillHubPublicationUploadToken) Reveal() string   { return t.value }
func (t SkillHubPublicationUploadToken) String() string   { return "[REDACTED publication upload token]" }
func (t SkillHubPublicationUploadToken) GoString() string { return t.String() }
func (t SkillHubPublicationUploadToken) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

// Internal record, not an HTTP or task-status projection. It is an upload-only
// authority observation, never a capture proof, Cloud task or commit permit.
type SkillHubPublicationUploadGrant struct {
	ID             string
	Input          SkillHubPublicationUploadInput
	Epoch          int
	IdempotencyKey string
	CreatedAt      time.Time
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

// Optional PostgreSQL foundation only: no public issuer/introspection endpoint
// or runtime consumer is registered yet. External integration must authenticate
// the Cloud peer independently and serialize task acceptance/cancellation.
type SkillHubPublicationUploadGrantStore interface {
	IssueSkillHubPublicationUploadGrant(context.Context, SkillHubPublicationUploadInput) (*SkillHubPublicationUploadGrant, SkillHubPublicationUploadToken, error)
	CheckSkillHubPublicationUploadGrant(context.Context, SkillHubPublicationUploadToken, SkillHubPublicationUploadInput, int) (*SkillHubPublicationUploadGrant, error)
}
