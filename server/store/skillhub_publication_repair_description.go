package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const SkillHubPublicationDescriptionDraftMaxBytes = 8192

func ValidSkillHubPublicationDescriptionKey(key string) bool {
	return publicationSHA256.MatchString(key)
}

// The selector comes from a verified description_missing issue, not a model's
// file path. SKILL.md selectors identify the candidate's exact entrypoint index.
type SkillHubPublicationDescriptionProposal struct {
	RelativePath string `json:"relativePath"`
	Field        string `json:"field"`
	EntryIndex   *int64 `json:"entryIndex,omitempty"`
	Text         string `json:"text"`
}

func (p SkillHubPublicationDescriptionProposal) Validate() error {
	if p.Field != "description" || len(p.Text) > SkillHubPublicationDescriptionDraftMaxBytes ||
		!utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) == "" ||
		strings.ContainsFunc(p.Text, func(r rune) bool { return (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 127 }) {
		return ErrSkillHubPublicationInvalid
	}
	switch p.RelativePath {
	case "SKILL.md":
		if p.EntryIndex != nil && *p.EntryIndex >= 0 && *p.EntryIndex <= 9007199254740991 {
			return nil
		}
	case "skill.json":
		if p.EntryIndex == nil {
			return nil
		}
	}
	return ErrSkillHubPublicationInvalid
}

type SkillHubPublicationDescriptionDraftInput struct {
	TaskID            string                                 `json:"taskId"`
	TaskIdentityHash  string                                 `json:"taskIdentityHash"`
	Round             int                                    `json:"round"`
	RoundIdentityHash string                                 `json:"roundIdentityHash"`
	Evidence          SkillHubPublicationRepairEvidence      `json:"evidence"`
	Proposal          SkillHubPublicationDescriptionProposal `json:"proposal"`
}

func (in SkillHubPublicationDescriptionDraftInput) IdentityHash() (string, error) {
	if !publicationRepairID.MatchString(in.TaskID) || !publicationSHA256.MatchString(in.TaskIdentityHash) ||
		!publicationSHA256.MatchString(in.RoundIdentityHash) || in.Round < 1 || in.Round > SkillHubPublicationRepairMaxRounds ||
		in.Evidence.Validate() != nil || in.Proposal.Validate() != nil {
		return "", ErrSkillHubPublicationInvalid
	}
	return repairIdentity("catsco-publication-description-draft-v1", in)
}

type SkillHubPublicationDescriptionDraft struct {
	EntryID         string
	Input           SkillHubPublicationDescriptionDraftInput
	ConfirmationKey string
	CreatedAt       time.Time
	ConfirmedAt     *time.Time
}

// Confirmation is retained evidence about exact text, not permission to edit a
// live Skill, resume a model call, spend another round, or publish a package.
func (d SkillHubPublicationDescriptionDraft) ValidateFor(t SkillHubPublicationRepairTask) error {
	hash, err := d.Input.IdentityHash()
	taskHash, taskErr := t.Input.IdentityHash()
	if err != nil || taskErr != nil || taskHash != t.IdentityHash || d.EntryID != t.Input.Receipt.Query.EntryID ||
		d.Input.TaskID != t.ID || d.Input.TaskIdentityHash != taskHash || d.ConfirmationKey != hash ||
		d.CreatedAt.IsZero() || (d.ConfirmedAt != nil && (d.ConfirmedAt.IsZero() || d.ConfirmedAt.Before(d.CreatedAt))) ||
		d.Input.Round != len(t.Rounds) || len(t.Rounds) == 0 {
		return ErrSkillHubPublicationConflict
	}
	r := t.Rounds[len(t.Rounds)-1]
	roundHash, roundErr := r.Lease.Input.IdentityHash()
	if r.Lease.Validate() != nil || r.Lease.EntryID != d.EntryID || r.Lease.TaskID != t.ID || r.Lease.Round != d.Input.Round ||
		roundErr != nil || roundHash != d.Input.RoundIdentityHash || d.Input.Evidence != r.Lease.Input.Evidence ||
		r.Result == nil || r.Result.Outcome != "waiting_confirmation" || r.Result.Code != "" || r.Result.Evidence != d.Input.Evidence {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

// Optional trusted-executor/human adapter interface. Proposing atomically stops
// the current round at waiting_confirmation. Only a verified report adapter may
// propose; only an explicit active-human JWT action may call Confirm. There is
// no model/RPC tool, executor registration, candidate writer or budget reset.
type SkillHubPublicationRepairDescriptionStore interface {
	ProposeSkillHubPublicationDescription(context.Context, int64, SkillHubPublicationRepairLease, SkillHubPublicationDescriptionProposal) (*SkillHubPublicationDescriptionDraft, error)
	GetSkillHubPublicationDescription(context.Context, int64, string) (*SkillHubPublicationDescriptionDraft, error)
	ConfirmSkillHubPublicationDescription(context.Context, int64, string, string) (*SkillHubPublicationDescriptionDraft, error)
}
