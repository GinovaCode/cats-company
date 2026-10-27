package store

import (
	"strings"
	"testing"
)

func TestPublicationDescriptionAcceptanceExactBinding(t *testing.T) {
	app, _ := descriptionApplicationFixture(t)
	result := SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: 100,
		Derivation: SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: strings.Repeat("6", 64), LimitsFingerprint: strings.Repeat("7", 64),
			DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
	app.Result, app.AppliedAt = &result, &app.CreatedAt
	valid := SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID, Result: result, AcceptedAt: app.CreatedAt,
		Report: PublicationValidationReport{SchemaVersion: "1", RuleVersion: "skillhub-publish-v1.0.0", Profile: "skillhub-publish-v1",
			ReportID: "12345678-1234-4123-8123-123456789abc", Outcome: "passed", Coverage: "complete", CandidateHash: result.Derivation.DerivedCandidateHash,
			LimitsFingerprint: result.Derivation.LimitsFingerprint, Issues: []PublicationValidationIssue{}, NextAction: "none"}}
	valid.IdentityHash, _ = valid.Digest()
	if err := valid.ValidateFor(app); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*SkillHubPublicationDescriptionAcceptance)
	}{
		{"application", func(a *SkillHubPublicationDescriptionAcceptance) {
			a.ApplicationID = "pubdesc_" + strings.Repeat("f", 64)
		}},
		{"result", func(a *SkillHubPublicationDescriptionAcceptance) { a.Result.RequestBytes++ }},
		{"candidate", func(a *SkillHubPublicationDescriptionAcceptance) {
			a.Report.CandidateHash = app.Input.Receipt.Query.SourceSnapshotHash
		}},
		{"limits", func(a *SkillHubPublicationDescriptionAcceptance) {
			a.Report.LimitsFingerprint = strings.Repeat("f", 64)
		}},
		{"truncated", func(a *SkillHubPublicationDescriptionAcceptance) { a.Report.Truncated = true }},
		{"rejected", func(a *SkillHubPublicationDescriptionAcceptance) {
			a.Report.Outcome = "rejected"
			a.Report.NextAction = "review_report"
			a.Report.Truncated = true
		}},
		{"nil_issues", func(a *SkillHubPublicationDescriptionAcceptance) { a.Report.Issues = nil }},
		{"timestamp", func(a *SkillHubPublicationDescriptionAcceptance) { a.AcceptedAt = a.AcceptedAt.Add(-1) }},
		{"unsafe_location", func(a *SkillHubPublicationDescriptionAcceptance) {
			a.Report.Issues = []PublicationValidationIssue{{Code: "metadata.warning", Category: "format", RepairPolicy: "none", Message: "warning", Location: PublicationValidationLocation{RelativePath: "secrets.txt"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := valid
			tc.mutate(&a)
			a.IdentityHash, _ = a.Digest()
			if a.ValidateFor(app) == nil {
				t.Fatal("substitution accepted")
			}
		})
	}
	changed := valid
	changed.Report.ReportID = "22345678-1234-4123-8123-123456789abc"
	if changed.ValidateFor(app) == nil {
		t.Fatal("report changed without changing digest")
	}
	changed.IdentityHash, _ = changed.Digest()
	if changed.ValidateFor(app) != nil || changed.IdentityHash == valid.IdentityHash {
		t.Fatal("fresh report must have distinct retained identity")
	}
	app.Input.ConfirmationKey = strings.Repeat("f", 64)
	if valid.ValidateFor(app) == nil {
		t.Fatal("confirmation changed")
	}
}
