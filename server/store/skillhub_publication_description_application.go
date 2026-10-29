package store

import (
	"context"
	"time"
)

// The trusted executor constructs this from retained records, never a browser's
// confirmed flag, model output, digest, or another task's confirmation.
type SkillHubPublicationDescriptionApplicationInput struct {
	Receipt         SkillHubPublicationCloudReceipt          `json:"receipt"`
	Draft           SkillHubPublicationDescriptionDraftInput `json:"draft"`
	ConfirmationKey string                                   `json:"confirmationKey"`
}

func (in SkillHubPublicationDescriptionApplicationInput) IdentityHash() (string, error) {
	key, err := in.Draft.IdentityHash()
	if err != nil || key != in.ConfirmationKey || in.Receipt.Validate() != nil {
		return "", ErrSkillHubPublicationInvalid
	}
	return repairIdentity("catsco-publication-description-application-v1", in)
}

// Receipt from the private, deterministic Cloud derivation operation. The two
// Cloud canonical-JSON digests are NOT CatsCo's Go-JSON confirmation/evidence
// digests. This shape only checks correlation; it cannot prove retained bytes.
type SkillHubPublicationDescriptionDerivationReceipt struct {
	SchemaVersion         string `json:"schemaVersion"`
	Algorithm             string `json:"algorithm"`
	OriginalRequestHash   string `json:"originalRequestHash"`
	OriginalCandidateHash string `json:"originalCandidateHash"`
	ValidationDigest      string `json:"validationDigest"`
	ProposalDigest        string `json:"proposalDigest"`
	LimitsFingerprint     string `json:"limitsFingerprint"`
	DerivedRequestHash    string `json:"derivedRequestHash"`
	DerivedCandidateHash  string `json:"derivedCandidateHash"`
}

// Metadata only. Private candidate bytes remain with the candidate-retention
// adapter, under ApplicationID as its immutable idempotency key. It MUST prove
// and durably retain exact derived bytes before returning this result. A hash
// from an HTTP client/model or a validation-only response is not such a result.
type SkillHubPublicationDescriptionApplicationResult struct {
	ApplicationID string                                          `json:"applicationId"`
	RequestBytes  int64                                           `json:"requestBytes"`
	Derivation    SkillHubPublicationDescriptionDerivationReceipt `json:"derivation"`
}

func (r SkillHubPublicationDescriptionApplicationResult) ValidateFor(a SkillHubPublicationDescriptionApplication) error {
	d := r.Derivation
	if r.ApplicationID != a.ID || r.RequestBytes < 1 || r.RequestBytes > SkillHubPublicationCaptureMaxBytes ||
		d.SchemaVersion != "1" || d.Algorithm != "skillhub-description-derivation-v1" ||
		d.OriginalRequestHash != a.Input.Receipt.Query.RequestHash ||
		d.OriginalCandidateHash != a.Input.Draft.Evidence.CandidateHash ||
		d.DerivedRequestHash == d.OriginalRequestHash || d.DerivedCandidateHash == d.OriginalCandidateHash {
		return ErrSkillHubPublicationConflict
	}
	for _, h := range []string{d.OriginalRequestHash, d.OriginalCandidateHash, d.ValidationDigest,
		d.ProposalDigest, d.LimitsFingerprint, d.DerivedRequestHash, d.DerivedCandidateHash} {
		if !publicationSHA256.MatchString(h) {
			return ErrSkillHubPublicationInvalid
		}
	}
	return nil
}

type SkillHubPublicationDescriptionApplication struct {
	ID           string
	Input        SkillHubPublicationDescriptionApplicationInput
	IdentityHash string
	CreatedAt    time.Time
	Result       *SkillHubPublicationDescriptionApplicationResult
	AppliedAt    *time.Time
}

func (a SkillHubPublicationDescriptionApplication) ValidateFor(t SkillHubPublicationRepairTask, d SkillHubPublicationDescriptionDraft) error {
	h, err := a.Input.IdentityHash()
	key, draftErr := a.Input.Draft.IdentityHash()
	if err != nil || draftErr != nil || h != a.IdentityHash || a.ID != "pubdesc_"+h ||
		d.ValidateFor(t) != nil || d.ConfirmedAt == nil || key != d.ConfirmationKey ||
		a.Input.ConfirmationKey != d.ConfirmationKey || a.Input.Receipt != t.Input.Receipt ||
		a.CreatedAt.IsZero() || a.CreatedAt.Before(*d.ConfirmedAt) || (a.Result == nil) != (a.AppliedAt == nil) {
		return ErrSkillHubPublicationConflict
	}
	if a.Result != nil {
		if a.AppliedAt.IsZero() || a.AppliedAt.Before(a.CreatedAt) {
			return ErrSkillHubPublicationConflict
		}
		return a.Result.ValidateFor(a)
	}
	return nil
}

// Separate from repair rounds: preparing/replaying/completing an application
// never clears waiting_confirmation or resets/spends another repair round.
// There is no lease: deterministic derivation/retention may be replayed with
// the SAME application identity. This is never a paid-model retry mechanism.
// Each method rechecks current actor/Bot/body/private-task/cancel authority.
// No candidate bytes, network operation or model invocation is inside the DB.
type SkillHubPublicationDescriptionApplicationStore interface {
	PrepareSkillHubPublicationDescriptionApplication(context.Context, int64, string) (*SkillHubPublicationDescriptionApplication, error)
	GetSkillHubPublicationDescriptionApplication(context.Context, int64, string) (*SkillHubPublicationDescriptionApplication, error)
	CompleteSkillHubPublicationDescriptionApplication(context.Context, int64, SkillHubPublicationDescriptionApplication, SkillHubPublicationDescriptionApplicationResult) (*SkillHubPublicationDescriptionApplication, error)
}
