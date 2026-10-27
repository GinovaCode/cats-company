package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

// Lose responses only after the real PostgreSQL writes have committed. A new
// wrapper has no replay state: durable application/acceptance records recover it.
type descriptionContinuationLossStore struct {
	*postgres.Adapter
	loseApplication bool
	loseAcceptance  bool
}

func (db *descriptionContinuationLossStore) CompleteSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, app store.SkillHubPublicationDescriptionApplication, r store.SkillHubPublicationDescriptionApplicationResult) (*store.SkillHubPublicationDescriptionApplication, error) {
	got, err := db.Adapter.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, app, r)
	if err == nil && db.loseApplication {
		db.loseApplication = false
		return nil, context.DeadlineExceeded
	}
	return got, err
}

func (db *descriptionContinuationLossStore) AcceptSkillHubPublicationDescriptionValidation(ctx context.Context, actor int64, app store.SkillHubPublicationDescriptionApplication, report store.PublicationValidationReport) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	got, err := db.Adapter.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, app, report)
	if err == nil && db.loseAcceptance {
		db.loseAcceptance = false
		return nil, context.DeadlineExceeded
	}
	return got, err
}

// Called by the mandatory real JWT/HTTPS/PG publication contract after exact
// human confirmation. Cloud derivation/report/private bytes and Runtime source
// remain synthetic ports; this is not a live multi-repository/model E2E.
func verifyDescriptionContinuationPostgres(t *testing.T, db *postgres.Adapter, in store.SkillHubPublicationCommitInput, actor int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := in.Receipt.Query.EntryID
	raw := []byte("synthetic-private-derived-candidate")
	derive, report, copies := 0, 0, 0
	sourceChanged, changeAuthority := false, false
	ports := publicationDescriptionContinuationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
			if sourceChanged {
				return store.ErrSkillHubPublicationConflict
			}
			return nil
		},
		DeriveAndRetain: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
			derive++
			return store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: int64(len(raw)),
				Derivation: store.SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
					OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
					ValidationDigest: strings.Repeat("5", 64), ProposalDigest: publicationDescriptionProposalDigest(app.Input.Draft.Proposal), LimitsFingerprint: strings.Repeat("7", 64),
					DerivedRequestHash: publicationDescriptionSHA256Hex(string(raw)), DerivedCandidateHash: strings.Repeat("9", 64)}}, nil
		},
		ReadValidationReport: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
			report++
			r := descriptionDerivedReport(app)
			return &r, nil
		},
		ReadRetainedCopy: func(_ context.Context, _ store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
			copies++
			if changeAuthority {
				// Resolve the actual private task from durable records, not the
				// application hash (which is never a group identifier).
				task, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id)
				if err != nil {
					return nil, err
				}
				group, err := store.ParseSkillHubPublicationRepairTopic(task.Input.TopicID)
				if err != nil {
					return nil, err
				}
				if err := db.UpdateGroupKind(group, "standard"); err != nil {
					return nil, err
				}
			}
			return append([]byte(nil), raw...), nil
		},
	}
	loss := &descriptionContinuationLossStore{Adapter: db, loseApplication: true, loseAcceptance: true}
	for _, stage := range []string{"application", "acceptance"} {
		got, err := continueConfirmedPublicationDescription(ctx, loss, actor, id, ports)
		if !errors.Is(err, context.DeadlineExceeded) || got != nil {
			t.Fatal("response not lost after "+stage, err)
		}
	}
	// Reconstruct the orchestrator's store wrapper; recover entirely from PG.
	restarted := &descriptionContinuationLossStore{Adapter: db}
	first, err := continueConfirmedPublicationDescription(ctx, restarted, actor, id, ports)
	if err != nil || first == nil || first.checkpoint.Validate() != nil || string(first.bytes) != string(raw) {
		t.Fatal("durable continuation recovery", err)
	}
	again, err := continueConfirmedPublicationDescription(ctx, restarted, actor, id, ports)
	if err != nil || !reflect.DeepEqual(first, again) || derive != 1 || report != 1 || copies != 2 {
		t.Fatalf("repeated retained work: derive=%d report=%d copies=%d err=%v", derive, report, copies, err)
	}
	sourceChanged = true
	if got, err := continueConfirmedPublicationDescription(ctx, restarted, actor, id, ports); err == nil || got != nil {
		t.Fatal("retained acceptance bypassed changed source")
	}
	sourceChanged, changeAuthority = false, true
	if got, err := continueConfirmedPublicationDescription(ctx, restarted, actor, id, ports); err == nil || got != nil {
		t.Fatal("handoff bypassed changed task authority")
	}
	group, _ := store.ParseSkillHubPublicationRepairTopic(first.checkpoint.Task.Input.TopicID)
	if err := db.UpdateGroupKind(group, "agent_task"); err != nil {
		t.Fatal(err)
	}
	changeAuthority = false
	if got, err := continueConfirmedPublicationDescription(ctx, restarted, actor, id, ports); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("could not recover after authority restoration", err)
	}
	task, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id)
	if err != nil || task == nil || len(task.Rounds) != 1 || task.State() != "waiting_confirmation" || derive != 1 || report != 1 {
		t.Fatal("continuation changed budget/history", err)
	}
	local, err := db.GetSkillHubPublicationCommitContext(ctx, actor, id, true)
	if err != nil || local == nil || local.Intent != nil || local.Receipt == nil || *local.Receipt != in.Receipt {
		t.Fatal("continuation published or changed original receipt", err)
	}
	t.Log("real PostgreSQL confirmed-description continuation: committed application/acceptance response losses, reconstructed coordinator recovery, same report/candidate, authority/source changes rejected, one immutable waiting round; synthetic Cloud/Runtime ports, no model/source write/publication")
}
