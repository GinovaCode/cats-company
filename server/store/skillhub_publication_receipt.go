package store

import (
	"context"
	"regexp"
)

var publicationReceiptGrantID = regexp.MustCompile(`^pubgrant_[a-f0-9]{32}$`)
var publicationReceiptKey = regexp.MustCompile(`^pubcandidate_[a-f0-9]{32}$`)
var publicationReceiptTaskID = regexp.MustCompile(`^pubtask_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
var publicationReceiptEventID = regexp.MustCompile(`^pubevent_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// Historical acceptance evidence from the independently authenticated Cloud
// peer, NOT a live upload grant or permission to run/commit a publication.
// Query.Epoch is the immutable FIRST acceptance epoch, not the latest renewal.
// Issuer/Audience are configured locally. No user/Bot identity is caller chosen.
type SkillHubPublicationCloudReceipt struct {
	Query          SkillHubPublicationUploadQuery
	DelegationID   string
	IdempotencyKey string
	TaskID         string
	EventID        string
}

func (r SkillHubPublicationCloudReceipt) Validate() error {
	if r.Query.Validate() != nil || !publicationReceiptGrantID.MatchString(r.DelegationID) ||
		!publicationReceiptKey.MatchString(r.IdempotencyKey) || !publicationReceiptTaskID.MatchString(r.TaskID) ||
		!publicationReceiptEventID.MatchString(r.EventID) {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// This snapshot says only whether the local intent was already cancelled when
// evidence was recorded. False is NOT live authority; true is NOT remote cancel
// confirmation. Cloud must hold the task in either case pending a commit permit.
type SkillHubPublicationCloudReceiptResult struct {
	Receipt              SkillHubPublicationCloudReceipt
	LocalIntentCancelled bool
}

type SkillHubPublicationCloudReceiptStore interface {
	RecordSkillHubPublicationCloudReceipt(context.Context, SkillHubPublicationCloudReceipt) (*SkillHubPublicationCloudReceiptResult, error)
}
