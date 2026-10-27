package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionContinuationTestStore struct {
	descriptionAcceptanceTestStore
	task      store.SkillHubPublicationRepairTask
	draft     store.SkillHubPublicationDescriptionDraft
	completes int
	bundles   int
	lose      string
	onBundle  func(int) error
}

func (db *descriptionContinuationTestStore) PrepareSkillHubPublicationDescriptionApplication(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionApplication, error) {
	return db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
}

func (db *descriptionContinuationTestStore) CompleteSkillHubPublicationDescriptionApplication(_ context.Context, _ int64, app store.SkillHubPublicationDescriptionApplication, r store.SkillHubPublicationDescriptionApplicationResult) (*store.SkillHubPublicationDescriptionApplication, error) {
	db.completes++
	if db.app.ID != app.ID {
		return nil, store.ErrSkillHubPublicationConflict
	}
	db.app.Result = &r
	now := time.Now().UTC()
	db.app.AppliedAt = &now
	if db.lose == "application" {
		db.lose = ""
		return nil, context.DeadlineExceeded
	}
	return db.app, nil
}

func (db *descriptionContinuationTestStore) AcceptSkillHubPublicationDescriptionValidation(ctx context.Context, actor int64, app store.SkillHubPublicationDescriptionApplication, r store.PublicationValidationReport) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	a, err := db.descriptionAcceptanceTestStore.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, app, r)
	if err == nil && db.lose == "acceptance" {
		db.lose = ""
		return nil, context.DeadlineExceeded
	}
	return a, err
}

func (db *descriptionContinuationTestStore) GetSkillHubPublicationDescriptionContinuation(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionContinuation, error) {
	db.bundles++
	if db.onBundle != nil {
		if err := db.onBundle(db.bundles); err != nil {
			return nil, err
		}
	}
	if actor != 1 || id != db.app.Input.Receipt.Query.EntryID || db.accepted == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	c := store.SkillHubPublicationDescriptionContinuation{Task: db.task, Draft: db.draft, Application: *db.app, Acceptance: *db.accepted}
	return &c, nil
}

func descriptionContinuationFixture(t *testing.T) (*descriptionContinuationTestStore, publicationDescriptionContinuationPorts, *[]string) {
	t.Helper()
	raw := []byte("private-confirmed-derived-copy")
	app := completedDescriptionApplication(t, "https://hub.example", raw)
	result := *app.Result
	app.Result, app.AppliedAt = nil, nil
	d := app.Input.Draft
	created := app.CreatedAt.Add(-time.Second)
	confirmed := app.CreatedAt
	task := store.SkillHubPublicationRepairTask{ID: d.TaskID, IdentityHash: d.TaskIdentityHash, CreatedAt: created,
		Input: store.SkillHubPublicationRepairInput{Receipt: app.Input.Receipt, TopicID: "grp_123", Evidence: d.Evidence},
		Rounds: []store.SkillHubPublicationRepairRound{{Lease: store.SkillHubPublicationRepairLease{
			EntryID: app.Input.Receipt.Query.EntryID, TaskID: d.TaskID, Round: 1,
			Input:    store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: d.Evidence},
			WorkerID: "description-worker", Token: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Generation: 1, Until: app.CreatedAt.Add(time.Minute)},
			Result: &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: d.Evidence}}}}
	db := &descriptionContinuationTestStore{descriptionAcceptanceTestStore: descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}},
		task: task, draft: store.SkillHubPublicationDescriptionDraft{EntryID: app.Input.Receipt.Query.EntryID, Input: d,
			ConfirmationKey: app.Input.ConfirmationKey, CreatedAt: created, ConfirmedAt: &confirmed}}
	events := []string{}
	ports := publicationDescriptionContinuationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
			events = append(events, "source")
			return nil
		},
		DeriveAndRetain: func(context.Context, store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
			events = append(events, "derive")
			return result, nil
		},
		ReadValidationReport: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
			events = append(events, "report")
			r := descriptionDerivedReport(a)
			return &r, nil
		},
		ReadRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
			events = append(events, "copy")
			return append([]byte(nil), raw...), nil
		},
	}
	return db, ports, &events
}

func TestContinueConfirmedPublicationDescriptionRecovery(t *testing.T) {
	for _, lost := range []string{"none", "application", "acceptance", "handoff"} {
		t.Run(lost, func(t *testing.T) {
			db, ports, events := descriptionContinuationFixture(t)
			id := db.app.Input.Receipt.Query.EntryID
			db.lose = lost
			if lost == "handoff" {
				db.onBundle = func(n int) error {
					if n == 3 {
						return context.DeadlineExceeded
					}
					return nil
				}
			}
			first, err := continueConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
			if lost == "none" {
				if err != nil || first == nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || first != nil {
				t.Fatal("did not lose response", err)
			}
			got, err := continueConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
			if err != nil || got == nil || got.checkpoint.Validate() != nil || string(got.bytes) != "private-confirmed-derived-copy" {
				t.Fatal("recovery", err)
			}
			derive, report := 0, 0
			for _, event := range *events {
				if event == "derive" {
					derive++
				}
				if event == "report" {
					report++
				}
			}
			if derive != 1 || report != 1 || db.completes != 1 || db.writes != 1 || len(db.task.Rounds) != 1 || db.task.State() != "waiting_confirmation" {
				t.Fatalf("replayed operation or rewrote budget: %v", *events)
			}
			again, err := continueConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("replay changed handoff", err)
			}
		})
	}
}

func TestContinueConfirmedPublicationDescriptionFences(t *testing.T) {
	for _, kind := range []string{"source_after_copy", "changed_bytes", "missing_copy", "copy_error", "authority1", "authority2", "authority3", "changed_draft", "changed_task", "cancel_after_copy"} {
		t.Run(kind, func(t *testing.T) {
			db, ports, _ := descriptionContinuationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			changed := false
			originalSource, originalCopy := ports.VerifySource, ports.ReadRetainedCopy
			ports.VerifySource = func(ctx context.Context, a store.SkillHubPublicationDescriptionApplication) error {
				if kind == "source_after_copy" && changed {
					return store.ErrSkillHubPublicationConflict
				}
				return originalSource(ctx, a)
			}
			ports.ReadRetainedCopy = func(ctx context.Context, a store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
				raw, err := originalCopy(ctx, a)
				changed = true
				switch kind {
				case "changed_bytes":
					raw[0] = 'X'
				case "missing_copy":
					return nil, nil
				case "copy_error":
					return nil, context.DeadlineExceeded
				case "cancel_after_copy":
					cancel()
				}
				return raw, err
			}
			db.onBundle = func(n int) error {
				if kind == "authority1" && n == 1 || kind == "authority2" && n == 2 || kind == "authority3" && n == 3 {
					return store.ErrSkillHubPublicationDenied
				}
				if n == 3 {
					switch kind {
					case "changed_draft":
						db.draft.Input.Proposal.Text += " changed"
					case "changed_task":
						db.task.Rounds[0].Lease.WorkerID += "-other"
					}
				}
				return nil
			}
			got, err := continueConfirmedPublicationDescription(ctx, db, 1, db.app.Input.Receipt.Query.EntryID, ports)
			if err == nil || got != nil {
				t.Fatal("unsafe continuation returned", kind)
			}
		})
	}
}

func TestContinueConfirmedPublicationDescriptionOwnsSnapshots(t *testing.T) {
	db, ports, _ := descriptionContinuationFixture(t)
	derive, verify, read := ports.DeriveAndRetain, ports.VerifySource, ports.ReadRetainedCopy
	var buffer []byte
	ports.DeriveAndRetain = func(ctx context.Context, a store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
		r, err := derive(ctx, a)
		*a.Input.Draft.Proposal.EntryIndex = 99
		return r, err
	}
	ports.VerifySource = func(ctx context.Context, a store.SkillHubPublicationDescriptionApplication) error {
		if len(buffer) > 0 {
			buffer[0] = 'Z'
		}
		*a.Input.Draft.Proposal.EntryIndex = 88
		if a.Result != nil {
			a.Result.RequestBytes++
		}
		return verify(ctx, a)
	}
	ports.ReadRetainedCopy = func(ctx context.Context, a store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
		raw, err := read(ctx, a)
		buffer = raw
		return raw, err
	}
	got, err := continueConfirmedPublicationDescription(context.Background(), db, 1, db.app.Input.Receipt.Query.EntryID, ports)
	if err != nil || string(got.bytes) != "private-confirmed-derived-copy" {
		t.Fatal(err)
	}
	got.bytes[0] = 'X'
	*got.checkpoint.Application.Input.Draft.Proposal.EntryIndex = 77
	*got.checkpoint.Draft.ConfirmedAt = time.Time{}
	got.checkpoint.Task.Rounds[0].Result.Outcome = "validated"
	if *db.app.Input.Draft.Proposal.EntryIndex != 0 || db.draft.ConfirmedAt.IsZero() || db.task.State() != "waiting_confirmation" {
		t.Fatal("handoff aliases retained records")
	}
}

func TestContinueConfirmedPublicationDescriptionDeterministicRetentionLoss(t *testing.T) {
	db, ports, _ := descriptionContinuationFixture(t)
	derive := ports.DeriveAndRetain
	calls, applicationID := 0, ""
	ports.DeriveAndRetain = func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
		calls++
		if applicationID == "" {
			applicationID = app.ID
		} else if app.ID != applicationID {
			t.Fatal("retention recovery allocated another application")
		}
		r, err := derive(ctx, app)
		if calls == 1 {
			// The deterministic peer retained its copy but its reply was lost.
			return store.SkillHubPublicationDescriptionApplicationResult{}, context.DeadlineExceeded
		}
		return r, err
	}
	id := db.app.Input.Receipt.Query.EntryID
	if got, err := continueConfirmedPublicationDescription(context.Background(), db, 1, id, ports); !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatal("retention response not lost", err)
	}
	got, err := continueConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
	if err != nil || got == nil || calls != 2 || db.completes != 1 || db.writes != 1 || len(got.checkpoint.Task.Rounds) != 1 {
		t.Fatal("deterministic retention did not recover same checkpoint", err)
	}
}
