package store

import (
	"strings"
	"testing"
	"time"
)

func descriptionApplicationFixture(t *testing.T) (SkillHubPublicationDescriptionApplication, SkillHubPublicationRepairTask) {
	t.Helper()
	entry := "pubentry_" + strings.Repeat("a", 32)
	receipt := SkillHubPublicationCloudReceipt{Query: SkillHubPublicationUploadQuery{
		EntryID: entry, Issuer: "https://cats.example", Audience: "https://hub.example",
		Action: SkillHubPublicationUploadAction, Revision: 1, Epoch: 1,
		RequestHash: strings.Repeat("1", 64), SourceSnapshotHash: strings.Repeat("2", 64), RequestBytes: 100,
	}, DelegationID: "pubgrant_" + strings.Repeat("b", 32), IdempotencyKey: "pubcandidate_" + strings.Repeat("c", 32),
		TaskID: "pubtask_12345678-1234-4123-8123-123456789abc", EventID: "pubevent_12345678-1234-4123-8123-123456789abc"}
	taskInput := SkillHubPublicationRepairInput{Receipt: receipt, TopicID: "grp_123", Evidence: SkillHubPublicationRepairEvidence{
		CandidateHash: strings.Repeat("2", 64), ReportHash: strings.Repeat("3", 64), IssueSetHash: strings.Repeat("4", 64)}}
	taskHash, err := taskInput.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	roundInput := SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: taskInput.Evidence}
	roundHash, err := roundInput.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	draftInput := SkillHubPublicationDescriptionDraftInput{TaskID: "pubrepair_" + strings.Repeat("d", 32), TaskIdentityHash: taskHash, Round: 1,
		RoundIdentityHash: roundHash, Evidence: taskInput.Evidence,
		Proposal: SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read a document."}}
	key, err := draftInput.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := SkillHubPublicationRepairTask{ID: draftInput.TaskID, Input: taskInput, IdentityHash: taskHash, CreatedAt: now,
		Rounds: []SkillHubPublicationRepairRound{{Lease: SkillHubPublicationRepairLease{EntryID: entry, TaskID: draftInput.TaskID, Round: 1,
			Input: roundInput, WorkerID: "description-worker", Token: strings.Repeat("e", 32), Generation: 1, Until: now.Add(time.Minute)},
			Result: &SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: taskInput.Evidence}}}}
	draft := SkillHubPublicationDescriptionDraft{EntryID: entry, Input: draftInput, ConfirmationKey: key, CreatedAt: now}
	appInput := SkillHubPublicationDescriptionApplicationInput{Receipt: receipt, Draft: draftInput, ConfirmationKey: key}
	identity, err := appInput.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	confirmed := now.Add(time.Second)
	app := SkillHubPublicationDescriptionApplication{ID: "pubdesc_" + identity, Input: appInput, IdentityHash: identity, CreatedAt: confirmed, AppliedAt: nil}
	// Confirmation is represented by the draft timestamp only in the model test;
	// the database adapter additionally binds the immutable confirmation row.
	draft.ConfirmedAt = &confirmed
	return app, task
}

func TestSkillHubPublicationDescriptionApplicationIdentityAndResult(t *testing.T) {
	app, task := descriptionApplicationFixture(t)
	confirmed := app.CreatedAt.Add(-time.Millisecond)
	draft := SkillHubPublicationDescriptionDraft{EntryID: app.Input.Receipt.Query.EntryID, Input: app.Input.Draft, ConfirmationKey: app.Input.ConfirmationKey,
		CreatedAt: app.CreatedAt.Add(-time.Second), ConfirmedAt: &confirmed}
	if err := app.ValidateFor(task, draft); err != nil {
		t.Fatal("valid application rejected", err)
	}
	result := SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: 100,
		Derivation: SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: strings.Repeat("6", 64), LimitsFingerprint: strings.Repeat("7", 64),
			DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
	if err := result.ValidateFor(app); err != nil {
		t.Fatal("valid result rejected", err)
	}
	for _, bad := range []SkillHubPublicationDescriptionApplicationResult{
		{ApplicationID: "pubdesc_" + strings.Repeat("0", 64), RequestBytes: 100, Derivation: result.Derivation},
		{ApplicationID: app.ID, RequestBytes: 100, Derivation: func() SkillHubPublicationDescriptionDerivationReceipt {
			d := result.Derivation
			d.DerivedCandidateHash = d.OriginalCandidateHash
			return d
		}()},
	} {
		if err := bad.ValidateFor(app); err == nil {
			t.Fatal("substituted result accepted")
		}
	}
	app.Result = &result
	app.AppliedAt = &app.CreatedAt
	if err := app.ValidateFor(task, draft); err != nil {
		t.Fatal("completed application rejected", err)
	}
}
