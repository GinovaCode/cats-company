package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type applicationTestStore struct {
	app      *store.SkillHubPublicationDescriptionApplication
	prepare  int
	get      int
	complete int
}

func (s *applicationTestStore) PrepareSkillHubPublicationDescriptionApplication(context.Context, int64, string) (*store.SkillHubPublicationDescriptionApplication, error) {
	s.prepare++
	return s.app, nil
}
func (s *applicationTestStore) GetSkillHubPublicationDescriptionApplication(context.Context, int64, string) (*store.SkillHubPublicationDescriptionApplication, error) {
	s.get++
	return s.app, nil
}
func (s *applicationTestStore) CompleteSkillHubPublicationDescriptionApplication(_ context.Context, _ int64, _ store.SkillHubPublicationDescriptionApplication, result store.SkillHubPublicationDescriptionApplicationResult) (*store.SkillHubPublicationDescriptionApplication, error) {
	s.complete++
	now := time.Now()
	s.app.Result = &result
	s.app.AppliedAt = &now
	return s.app, nil
}

func applicationTestFixture(t *testing.T) (*applicationTestStore, store.SkillHubPublicationDescriptionApplicationResult) {
	t.Helper()
	entry := "pubentry_" + strings.Repeat("a", 32)
	receipt := store.SkillHubPublicationCloudReceipt{Query: store.SkillHubPublicationUploadQuery{
		EntryID: entry, Issuer: "https://cats.example", Audience: "https://hub.example", Action: store.SkillHubPublicationUploadAction,
		Revision: 1, Epoch: 1, RequestHash: strings.Repeat("1", 64), SourceSnapshotHash: strings.Repeat("2", 64), RequestBytes: 100,
	}, DelegationID: "pubgrant_" + strings.Repeat("b", 32), IdempotencyKey: "pubcandidate_" + strings.Repeat("c", 32),
		TaskID: "pubtask_12345678-1234-4123-8123-123456789abc", EventID: "pubevent_12345678-1234-4123-8123-123456789abc"}
	evidence := store.SkillHubPublicationRepairEvidence{CandidateHash: strings.Repeat("2", 64), ReportHash: strings.Repeat("3", 64), IssueSetHash: strings.Repeat("4", 64)}
	taskInput := store.SkillHubPublicationRepairInput{Receipt: receipt, TopicID: "grp_123", Evidence: evidence}
	taskHash, _ := taskInput.IdentityHash()
	round := store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: evidence}
	roundHash, _ := round.IdentityHash()
	index := int64(0)
	draft := store.SkillHubPublicationDescriptionDraftInput{TaskID: "pubrepair_" + strings.Repeat("d", 32), TaskIdentityHash: taskHash, Round: 1, RoundIdentityHash: roundHash, Evidence: evidence,
		Proposal: store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read a document."}}
	key, _ := draft.IdentityHash()
	input := store.SkillHubPublicationDescriptionApplicationInput{Receipt: receipt, Draft: draft, ConfirmationKey: key}
	identity, _ := input.IdentityHash()
	app := &store.SkillHubPublicationDescriptionApplication{ID: "pubdesc_" + identity, Input: input, IdentityHash: identity, CreatedAt: time.Now().Add(-time.Second)}
	result := store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: 100,
		Derivation: store.SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: receipt.Query.RequestHash, OriginalCandidateHash: evidence.CandidateHash, ValidationDigest: strings.Repeat("5", 64), ProposalDigest: strings.Repeat("6", 64), LimitsFingerprint: strings.Repeat("7", 64), DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
	return &applicationTestStore{app: app}, result
}

func TestApplyPublicationDescriptionReplaysRetainedResult(t *testing.T) {
	db, result := applicationTestFixture(t)
	verify, derive := 0, 0
	ports := publicationDescriptionApplicationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { verify++; return nil },
		DeriveAndRetain: func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
			derive++
			return result, nil
		},
	}
	first, err := applyPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, ports)
	if err != nil || first.Result == nil || db.complete != 1 || derive != 1 || verify != 2 {
		t.Fatalf("first application: app=%#v err=%v prepare=%d complete=%d derive=%d verify=%d", first, err, db.prepare, db.complete, derive, verify)
	}
	second, err := applyPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, ports)
	if err != nil || second.Result == nil || db.complete != 1 || derive != 1 || verify != 2 {
		t.Fatalf("replay reran application: app=%#v err=%v complete=%d derive=%d verify=%d", second, err, db.complete, derive, verify)
	}
}

func TestApplyPublicationDescriptionChecksCancellation(t *testing.T) {
	db, _ := applicationTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := applyPublicationDescription(ctx, db, 1, db.app.Input.Receipt.Query.EntryID, publicationDescriptionApplicationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
			t.Fatal("source verification after cancellation")
			return nil
		},
		DeriveAndRetain: func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
			t.Fatal("derivation after cancellation")
			return store.SkillHubPublicationDescriptionApplicationResult{}, nil
		},
	})
	if err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
