package store

import (
	"context"
	"time"
)

// Query contains no claimed human/Bot identity or source selectors. Issuer and
// Audience come from configured service-peer policy, never request JSON.
type SkillHubPublicationUploadQuery struct {
	EntryID            string
	Issuer             string
	Audience           string
	Action             string
	Revision           int
	Epoch              int
	RequestHash        string
	SourceSnapshotHash string
	RequestBytes       int64
}

func (q SkillHubPublicationUploadQuery) Validate() error {
	if !ValidSkillHubPublicationDeliveryEntryID(q.EntryID) || !ValidSkillHubPublicationOrigin(q.Issuer) ||
		!ValidSkillHubPublicationOrigin(q.Audience) || q.Issuer == q.Audience || q.Action != SkillHubPublicationUploadAction ||
		q.Revision != 1 || q.Epoch < 1 || q.Epoch > SkillHubPublicationUploadMaxIssues ||
		!publicationSHA256.MatchString(q.RequestHash) || !publicationSHA256.MatchString(q.SourceSnapshotHash) ||
		q.RequestBytes < 1 || q.RequestBytes > SkillHubPublicationUploadMaxBytes {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

func (in SkillHubPublicationUploadInput) Query(epoch int) SkillHubPublicationUploadQuery {
	return SkillHubPublicationUploadQuery{EntryID: in.EntryID, Issuer: in.Issuer, Audience: in.Audience, Action: in.Action,
		Revision: in.Revision, Epoch: epoch, RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes}
}

type SkillHubPublicationUploadObservation struct {
	Grant      *SkillHubPublicationUploadGrant
	ObservedAt time.Time
}

// The caller must first authenticate the configured Cloud peer independently.
// Only a matching live token yields canonical stored identity. This observation
// is neither a durable authority reservation nor permission to commit a package.
type SkillHubPublicationUploadIntrospector interface {
	IntrospectSkillHubPublicationUploadGrant(context.Context, SkillHubPublicationUploadToken, SkillHubPublicationUploadQuery) (*SkillHubPublicationUploadObservation, error)
}
