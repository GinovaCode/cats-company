package store

import "context"

// Private server-only context. Never accept a receipt, publisher or target from
// browser/model input. Reads do not create authorization or contact Cloud.
type SkillHubPublicationCommitContext struct {
	Entry   SkillHubPublicationEntry
	Receipt *SkillHubPublicationCloudReceipt
	Intent  *SkillHubPublicationCommitIntent
}

type SkillHubPublicationCommitContextReader interface {
	// current=true also requires current ownership/body/enabled state and an
	// uncancelled entry. Historical status remains original-active-human only.
	GetSkillHubPublicationCommitContext(context.Context, int64, string, bool) (*SkillHubPublicationCommitContext, error)
}
