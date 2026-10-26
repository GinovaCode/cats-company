package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func descriptionPlanHandoff(t *testing.T) (*descriptionContinuationTestStore, *publicationDescriptionContinuation) {
	t.Helper()
	db, ports, _ := descriptionContinuationFixture(t)
	handoff, err := continueConfirmedPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, ports)
	if err != nil {
		t.Fatal(err)
	}
	db.bundles = 0
	return db, handoff
}

func TestPlanConfirmedPublicationDescriptionRecovery(t *testing.T) {
	db, handoff := descriptionPlanHandoff(t)
	before := snapshotDescriptionContinuation(handoff.checkpoint)
	plan := descriptionPlanFixture(*db.app, false)
	calls, verifies := 0, 0
	ports := publicationDescriptionReleasePlanPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { verifies++; return nil },
		PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
			calls++
			if !reflect.DeepEqual(app, before.Application) || confirm {
				t.Fatal("confirmation or application changed")
			}
			if calls == 1 {
				return nil, context.DeadlineExceeded
			} // Cloud may already have retained the plan.
			copy := plan
			return &copy, nil
		},
	}
	id := db.app.Input.Receipt.Query.EntryID
	if got, err := planConfirmedPublicationDescription(context.Background(), db, 1, id, handoff, false, ports); got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := planConfirmedPublicationDescription(context.Background(), db, 1, id, handoff, false, ports)
		if err != nil || got == nil || got.plan != plan || !reflect.DeepEqual(got.checkpoint, before) {
			t.Fatal("recovery changed release", err)
		}
		if got.plan.PublicationReceipt.SourceSnapshotHash == got.plan.Target.CandidateHash {
			t.Fatal("rewrote original receipt")
		}
	}
	if calls != 3 || verifies != 5 || db.completes != 1 || db.writes != 1 || !reflect.DeepEqual(handoff.checkpoint, before) || len(db.task.Rounds) != 1 || db.task.State() != "waiting_confirmation" {
		t.Fatal("changed history/budget")
	}
}

func TestPlanConfirmedPublicationDescriptionFences(t *testing.T) {
	for _, mode := range []string{"bad-bytes", "bad-checkpoint", "authority1", "authority2", "authority3", "source1", "source2", "changed-task", "changed-draft", "changed-acceptance", "changed-app", "candidate", "receipt", "version-policy", "missing-plan", "plan-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, handoff := descriptionPlanHandoff(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, verifies := 0, 0
			if mode == "bad-bytes" {
				handoff.bytes[0] = 'X'
			}
			if mode == "bad-checkpoint" {
				handoff.checkpoint.Task.Rounds = nil
			}
			db.onBundle = func(n int) error {
				if mode == "authority1" && n == 1 || mode == "authority2" && n == 2 || mode == "authority3" && n == 3 {
					return store.ErrSkillHubPublicationDenied
				}
				return nil
			}
			ports := publicationDescriptionReleasePlanPorts{
				VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
					verifies++
					if mode == "source1" && verifies == 1 || mode == "source2" && verifies == 2 {
						return store.ErrSkillHubPublicationConflict
					}
					return nil
				},
				PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, _ bool) (*publicationDescriptionReleasePlan, error) {
					calls++
					plan := descriptionPlanFixture(app, false)
					switch mode {
					case "changed-task":
						db.task.Rounds[0].Lease.WorkerID += "-changed"
					case "changed-draft":
						db.draft.Input.Proposal.Text += " changed"
					case "changed-acceptance":
						db.accepted.AcceptedAt = db.accepted.AcceptedAt.Add(1)
					case "changed-app":
						db.app.AppliedAt = nil
					case "candidate":
						plan.Target.CandidateHash = app.Input.Receipt.Query.SourceSnapshotHash
					case "receipt":
						plan.PublicationReceipt.SourceSnapshotHash = plan.Target.CandidateHash
					case "version-policy":
						plan.Target.ConfirmVersionPublish = true
					case "missing-plan":
						return nil, nil
					case "plan-error":
						return nil, context.DeadlineExceeded
					case "cancel":
						cancel()
					}
					return &plan, nil
				},
			}
			got, err := planConfirmedPublicationDescription(ctx, db, 1, db.app.Input.Receipt.Query.EntryID, handoff, false, ports)
			if err == nil || got != nil {
				t.Fatal("unsafe plan accepted", mode)
			}
			if (mode == "bad-bytes" || mode == "bad-checkpoint" || mode == "authority1" || mode == "authority2" || mode == "source1") && calls != 0 {
				t.Fatal("untrusted handoff dispatched")
			}
		})
	}
}

func TestPlanConfirmedPublicationDescriptionOwnsSnapshots(t *testing.T) {
	db, handoff := descriptionPlanHandoff(t)
	baseline := snapshotDescriptionContinuation(handoff.checkpoint)
	plan := descriptionPlanFixture(baseline.Application, false)
	want := plan
	checks := 0
	ports := publicationDescriptionReleasePlanPorts{
		VerifySource: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication) error {
			checks++
			*app.Input.Draft.Proposal.EntryIndex = 99
			app.Result.RequestBytes++
			if checks == 2 {
				plan.Target.Version = "9.9.9"
			}
			return nil
		},
		PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, _ bool) (*publicationDescriptionReleasePlan, error) {
			*app.Input.Draft.Proposal.EntryIndex = 88
			app.Result.RequestBytes++
			return &plan, nil
		},
	}
	got, err := planConfirmedPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, handoff, false, ports)
	if err != nil || got == nil || got.plan != want || !reflect.DeepEqual(got.checkpoint, baseline) || !reflect.DeepEqual(handoff.checkpoint, baseline) {
		t.Fatal("peer alias changed release", err)
	}
}

func TestPlanConfirmedPublicationDescriptionInvalid(t *testing.T) {
	db, handoff := descriptionPlanHandoff(t)
	ports := publicationDescriptionReleasePlanPorts{}
	if got, err := planConfirmedPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, handoff, false, ports); got != nil || err != store.ErrSkillHubPublicationInvalid {
		t.Fatal(err)
	}
}
