package store

import (
	"context"
	"time"
)

// Historical validation evidence, not a repair lease, execution grant or commit
// permit. ApplicationID binds the exact original receipt, round and human text
// confirmation. Keeping this separate preserves immutable round history.
type SkillHubPublicationDescriptionAcceptance struct {
	ApplicationID string                                          `json:"applicationId"`
	Result        SkillHubPublicationDescriptionApplicationResult `json:"result"`
	Report        PublicationValidationReport                     `json:"report"`
	IdentityHash  string                                          `json:"identityHash"`
	AcceptedAt    time.Time                                       `json:"acceptedAt"`
}

func (a SkillHubPublicationDescriptionAcceptance) Digest() (string, error) {
	return repairIdentity("catsco-publication-description-acceptance-v1", struct {
		ApplicationID string                                          `json:"applicationId"`
		Result        SkillHubPublicationDescriptionApplicationResult `json:"result"`
		Report        PublicationValidationReport                     `json:"report"`
	}{a.ApplicationID, a.Result, a.Report})
}

func (a SkillHubPublicationDescriptionAcceptance) ValidateFor(app SkillHubPublicationDescriptionApplication) error {
	h, err := app.Input.IdentityHash()
	if err != nil || app.IdentityHash != h || app.ID != "pubdesc_"+h || app.Result == nil || app.AppliedAt == nil ||
		app.CreatedAt.IsZero() || app.AppliedAt.Before(app.CreatedAt) || app.Result.ValidateFor(app) != nil ||
		a.ApplicationID != app.ID || a.Result != *app.Result || a.AcceptedAt.IsZero() || a.AcceptedAt.Before(*app.AppliedAt) ||
		!ValidPublicationValidationReport(a.Report, a.Result.Derivation.DerivedCandidateHash) || a.Report.Truncated ||
		a.Report.Outcome != "passed" || a.Report.LimitsFingerprint != a.Result.Derivation.LimitsFingerprint {
		return ErrSkillHubPublicationConflict
	}
	digest, err := a.Digest()
	if err != nil || digest != a.IdentityHash {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

// Every read/write rechecks CURRENT human/Bot/body/private-topic/cancel authority
// and the exact completed application in one transaction. Network/source checks
// belong to the trusted coordinator; the DB never holds locks across them.
type SkillHubPublicationDescriptionAcceptanceStore interface {
	GetSkillHubPublicationDescriptionAcceptance(context.Context, int64, SkillHubPublicationDescriptionApplication) (*SkillHubPublicationDescriptionAcceptance, error)
	AcceptSkillHubPublicationDescriptionValidation(context.Context, int64, SkillHubPublicationDescriptionApplication, PublicationValidationReport) (*SkillHubPublicationDescriptionAcceptance, error)
}
