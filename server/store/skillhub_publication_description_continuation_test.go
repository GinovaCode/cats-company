package store

import (
	"strings"
	"testing"
)

func descriptionContinuationFixture(t *testing.T) SkillHubPublicationDescriptionContinuation {
	t.Helper()
	app, task := descriptionApplicationFixture(t)
	confirmed := app.CreatedAt
	draft := SkillHubPublicationDescriptionDraft{EntryID: app.Input.Receipt.Query.EntryID, Input: app.Input.Draft,
		ConfirmationKey: app.Input.ConfirmationKey, CreatedAt: task.CreatedAt, ConfirmedAt: &confirmed}
	app.Result = &SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: 100,
		Derivation: SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: strings.Repeat("6", 64), LimitsFingerprint: strings.Repeat("7", 64),
			DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
	app.AppliedAt = &confirmed
	accepted := SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID, Result: *app.Result, AcceptedAt: confirmed,
		Report: PublicationValidationReport{SchemaVersion: "1", RuleVersion: "skillhub-publish-v1.0.0", Profile: "skillhub-publish-v1",
			ReportID: "12345678-1234-4123-8123-123456789abc", Outcome: "passed", Coverage: "complete", CandidateHash: app.Result.Derivation.DerivedCandidateHash,
			LimitsFingerprint: app.Result.Derivation.LimitsFingerprint, Issues: []PublicationValidationIssue{}, NextAction: "none"}}
	accepted.IdentityHash, _ = accepted.Digest()
	c := SkillHubPublicationDescriptionContinuation{Task: task, Draft: draft, Application: app, Acceptance: accepted}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPublicationDescriptionContinuationBindsCompleteHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*SkillHubPublicationDescriptionContinuation)
	}{
		{"no_confirmation", func(c *SkillHubPublicationDescriptionContinuation) { c.Draft.ConfirmedAt = nil }},
		{"other_task", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.ID += "a" }},
		{"task_input", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.Input.TopicID = "grp_999" }},
		{"draft_text", func(c *SkillHubPublicationDescriptionContinuation) { c.Draft.Input.Proposal.Text += " changed" }},
		{"round_budget", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.Rounds = nil }},
		{"round_identity", func(c *SkillHubPublicationDescriptionContinuation) {
			c.Task.Rounds[0].Lease.Input.RequestID += "-other"
		}},
		{"round_task", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.Rounds[0].Lease.TaskID += "a" }},
		{"round_result", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.Rounds[0].Result = nil }},
		{"round_state", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.Rounds[0].Result.Outcome = "validated" }},
		{"creation_time", func(c *SkillHubPublicationDescriptionContinuation) { c.Task.CreatedAt = c.Draft.CreatedAt.Add(1) }},
		{"application", func(c *SkillHubPublicationDescriptionContinuation) { c.Application.Result = nil }},
		{"acceptance", func(c *SkillHubPublicationDescriptionContinuation) {
			c.Acceptance.IdentityHash = strings.Repeat("f", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := descriptionContinuationFixture(t)
			tc.edit(&c)
			if c.Validate() == nil {
				t.Fatal("substituted continuation accepted")
			}
		})
	}
}

func TestPublicationDescriptionContinuationDoesNotUnlockOldRound(t *testing.T) {
	c := descriptionContinuationFixture(t)
	if c.Task.State() != "waiting_confirmation" || c.Acceptance.Report.Outcome != "passed" {
		t.Fatal("validated copy must not replace immutable waiting history")
	}
	// A purported earlier round that did not make progress cannot be hidden by
	// appending a valid-looking final confirmation round.
	earlier := c.Task.Rounds[0]
	earlier.Result = &SkillHubPublicationRepairResult{Outcome: "rejected", Evidence: earlier.Lease.Input.Evidence}
	c.Task.Rounds = append([]SkillHubPublicationRepairRound{earlier}, c.Task.Rounds...)
	c.Task.Rounds[1].Lease.Round = 2
	c.Draft.Input.Round = 2
	c.Draft.ConfirmationKey, _ = c.Draft.Input.IdentityHash()
	c.Application.Input.Draft = c.Draft.Input
	c.Application.Input.ConfirmationKey = c.Draft.ConfirmationKey
	c.Application.IdentityHash, _ = c.Application.Input.IdentityHash()
	c.Application.ID = "pubdesc_" + c.Application.IdentityHash
	c.Application.Result.ApplicationID = c.Application.ID
	c.Acceptance.ApplicationID, c.Acceptance.Result = c.Application.ID, *c.Application.Result
	c.Acceptance.IdentityHash, _ = c.Acceptance.Digest()
	if c.Application.ValidateFor(c.Task, c.Draft) != nil {
		t.Fatal("fixture should pass final-draft-only checks")
	}
	if c.Validate() == nil {
		t.Fatal("earlier no-progress round bypassed")
	}
}
