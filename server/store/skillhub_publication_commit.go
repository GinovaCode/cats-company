package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var publicationSkillID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)
var publicationVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// Cloud-derived immutable release facts, not a browser/model supplied manifest.
// The future admission adapter must verify the publisher mapping, reservation,
// candidate and any required human confirmations before recording this intent.
// ManifestHash covers the exact frozen manifest; CandidateHash is the Cloud
// validator digest, distinct from receipt request/source hashes.
type SkillHubPublicationCommitTarget struct {
	CandidateHash         string `json:"candidateHash"`
	ManifestHash          string `json:"manifestHash"`
	PublisherUserID       string `json:"publisherUserId"`
	PublisherProfileID    string `json:"publisherProfileId"`
	Namespace             string `json:"namespace"`
	SkillID               string `json:"skillId"`
	Version               string `json:"version"`
	UploadID              string `json:"uploadId"`
	ReleaseAt             string `json:"releaseAt"`
	ConfirmVersionPublish bool   `json:"confirmVersionPublish"`
}

func (t SkillHubPublicationCommitTarget) Validate() error {
	if !publicationSHA256.MatchString(t.CandidateHash) || !publicationSHA256.MatchString(t.ManifestHash) ||
		!publicationReference.MatchString(t.PublisherUserID) || !publicationReference.MatchString(t.PublisherProfileID) ||
		!publicationReference.MatchString(t.UploadID) || len(t.SkillID) > 256 || !publicationSkillID.MatchString(t.SkillID) ||
		len(t.Namespace) > 128 || !strings.HasPrefix(t.SkillID, t.Namespace+"/") ||
		len(t.Version) > 160 || !publicationVersion.MatchString(t.Version) {
		return ErrSkillHubPublicationInvalid
	}
	// Match Cloud Date.toISOString exactly. No alternate timezone/precision can
	// give a logically identical release a different durable identity.
	at, err := time.Parse("2006-01-02T15:04:05.000Z", t.ReleaseAt)
	if err != nil || at.Format("2006-01-02T15:04:05.000Z") != t.ReleaseAt {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

type SkillHubPublicationCommitInput struct {
	Receipt SkillHubPublicationCloudReceipt
	Target  SkillHubPublicationCommitTarget
}

func (in SkillHubPublicationCommitInput) Validate() error {
	if in.Receipt.Validate() != nil || in.Target.Validate() != nil {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// A CatsCo-local integrity digest, not an interoperable JSON canonicalization
// protocol or bearer credential. Peers must replay the exact typed facts.
func (in SkillHubPublicationCommitInput) IdentityHash() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte("catsco-publication-commit-v1\x00"), b...))
	return hex.EncodeToString(h[:]), nil
}

type SkillHubPublicationCommitIntent struct {
	Input        SkillHubPublicationCommitInput
	IdentityHash string
	RequestedAt  time.Time
}

// Internal durable authorization evidence, NOT a bearer or proof of Cloud
// publication. Once issued it may be recovered after authority loss only for
// the exact original task/intent. Cloud must still serialize against its own
// cancellation and recheck Cloud publisher identity before writing anything.
type SkillHubPublicationCommitPermit struct {
	ID             string
	Intent         SkillHubPublicationCommitIntent
	AuthorityEpoch int
	AuthorizedAt   time.Time
}

// Optional storage foundation; no HTTP/RPC/worker adapter is registered here.
// Record requires a trusted human action and verified Cloud release facts.
// Authorize requires an independently authenticated configured Cloud peer;
// caller-supplied account IDs or an upload grant cannot authorize it. Adapters
// must not expose these methods through generic RPC/model tools.
type SkillHubPublicationCommitStore interface {
	RecordSkillHubPublicationCommitIntent(context.Context, int64, SkillHubPublicationCommitInput) (*SkillHubPublicationCommitIntent, error)
	AuthorizeSkillHubPublicationCommit(context.Context, SkillHubPublicationCommitInput) (*SkillHubPublicationCommitPermit, error)
	GetSkillHubPublicationCommitPermit(context.Context, int64, string) (*SkillHubPublicationCommitPermit, error)
}
