package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func repairBudgetMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000051_skillhub_publication_repair_budget." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationRepairMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(repairBudgetMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationRepairTables), " ") {
		t.Fatal("000051 differs from startup")
	}
}

func repairBudgetFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationUploadInput, store.SkillHubPublicationRepairInput, store.SkillHubPublicationCommitInput, int64) {
	t.Helper()
	db, dsn, in, c, other := commitFixture(t)
	group, err := db.CreateGroup("publication repair", in.Binding.ActorUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateGroupKind(group, "agent_task"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddGroupMember(group, in.Binding.BotUID, "member"); err != nil {
		t.Fatal(err)
	}
	r := store.SkillHubPublicationRepairInput{Receipt: c.Receipt, TopicID: fmt.Sprintf("grp_%d", group),
		Evidence: store.SkillHubPublicationRepairEvidence{CandidateHash: c.Receipt.Query.SourceSnapshotHash,
			ReportHash: strings.Repeat("c", 64), IssueSetHash: strings.Repeat("d", 64)}}
	return db, dsn, in, r, c, other
}

func createRepairBudget(t *testing.T, db *Adapter, actor int64, in store.SkillHubPublicationRepairInput) *store.SkillHubPublicationRepairTask {
	t.Helper()
	task, err := db.CreateSkillHubPublicationRepairTask(context.Background(), actor, in)
	if err != nil || task == nil || task.Input != in || task.ID == "" || task.CreatedAt.IsZero() {
		t.Fatalf("create repair: %+v %v", task, err)
	}
	return task
}

func claimRepairBudget(t *testing.T, db *Adapter, actor int64, id string, in store.SkillHubPublicationRepairRoundInput, worker string) *store.SkillHubPublicationRepairRound {
	t.Helper()
	r, err := db.ClaimSkillHubPublicationRepairRound(context.Background(), actor, id, in, worker)
	if err != nil || r == nil || r.Lease.Validate() != nil || r.Lease.Input != in {
		t.Fatalf("claim repair: %+v %v", r, err)
	}
	return r
}

func getRepairBudget(t *testing.T, db *Adapter, actor int64, id string) *store.SkillHubPublicationRepairTask {
	t.Helper()
	task, err := db.GetSkillHubPublicationRepairTask(context.Background(), actor, id)
	if err != nil || task == nil {
		t.Fatalf("get repair: %+v %v", task, err)
	}
	return task
}

func finishRepairBudget(t *testing.T, db *Adapter, actor int64, l store.SkillHubPublicationRepairLease, r store.SkillHubPublicationRepairResult) {
	t.Helper()
	if err := db.FinishSkillHubPublicationRepairRound(context.Background(), actor, l, r); err != nil {
		t.Fatal("finish repair", err)
	}
}

// Time travel only in this throwaway test schema, not a production operation.
func expireRepairBudget(t *testing.T, db *Adapter, id string) {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE skillhub_publication_repair_rounds DISABLE TRIGGER guard_skillhub_publication_repair_round`,
		`UPDATE skillhub_publication_repair_rounds SET lease_until=clock_timestamp()-interval '1 second' WHERE entry_id=$1 AND result IS NULL`,
		`ALTER TABLE skillhub_publication_repair_rounds ENABLE TRIGGER guard_skillhub_publication_repair_round`,
	} {
		var args []any
		if strings.Contains(q, "$1") {
			args = []any{id}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSkillHubPublicationRepairRecoveryBudgetAndReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	first := createRepairBudget(t, db, actor, input)
	if got := createRepairBudget(t, db, actor, input); !reflect.DeepEqual(got, first) {
		t.Fatal("duplicate task changed identity")
	}
	for _, mutate := range []func(*store.SkillHubPublicationRepairInput){
		func(x *store.SkillHubPublicationRepairInput) { x.TopicID = "grp_9223372036854775807" },
		func(x *store.SkillHubPublicationRepairInput) { x.Evidence.ReportHash = strings.Repeat("e", 64) },
		func(x *store.SkillHubPublicationRepairInput) { x.Receipt.Query.Epoch++ },
	} {
		x := input
		mutate(&x)
		if _, err := db.CreateSkillHubPublicationRepairTask(ctx, actor, x); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
			t.Fatal("duplicate start reset facts", err)
		}
	}
	roundInput := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
	r := claimRepairBudget(t, db, actor, id, roundInput, "worker-1")
	if got := claimRepairBudget(t, db, actor, id, roundInput, "worker-1"); !reflect.DeepEqual(got, r) {
		t.Fatal("lost claim response created another round")
	}
	if _, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, roundInput, "worker-2"); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("second worker acquired live lease", err)
	}
	old := r.Lease
	expireRepairBudget(t, db, id)
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	r = claimRepairBudget(t, reopened, actor, id, roundInput, "worker-2")
	if r.Lease.Round != 1 || r.Lease.Generation != 2 || r.Lease.Token == old.Token {
		t.Fatal("recovery reset round or failed to fence worker")
	}
	if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, old, store.SkillHubPublicationRepairResult{Outcome: "rejected", Evidence: input.Evidence}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("old worker finished reclaimed round", err)
	}
	for number := 1; number <= store.SkillHubPublicationRepairMaxRounds; number++ {
		if number > 1 {
			roundInput.RequestID = fmt.Sprintf("repair-round-%d", number)
			stale := roundInput
			stale.Evidence = input.Evidence
			if _, err := reopened.ClaimSkillHubPublicationRepairRound(ctx, actor, id, stale, "worker-2"); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal("new round reused stale evidence", err)
			}
			r = claimRepairBudget(t, reopened, actor, id, roundInput, "worker-2")
		}
		if r.Lease.Round != number {
			t.Fatal("wrong round number")
		}
		next := r.Lease.Input.Evidence
		next.CandidateHash = strings.Repeat(fmt.Sprintf("%x", number+4), 64)
		next.ReportHash = strings.Repeat(fmt.Sprintf("%x", number+8), 64)
		result := store.SkillHubPublicationRepairResult{Outcome: "rejected", Evidence: next}
		finishRepairBudget(t, reopened, actor, r.Lease, result)
		finishRepairBudget(t, reopened, actor, r.Lease, result)
		changed := result
		changed.Evidence.ReportHash = strings.Repeat("f", 64)
		if err := reopened.FinishSkillHubPublicationRepairRound(ctx, actor, r.Lease, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
			t.Fatal("completed checkpoint overwritten", err)
		}
		if got := claimRepairBudget(t, reopened, actor, id, r.Lease.Input, "worker-replay"); got.Result == nil || *got.Result != result {
			t.Fatal("completed replay lost result")
		}
		roundInput.Evidence = next
	}
	got := getRepairBudget(t, reopened, actor, id)
	if got.ID != first.ID || len(got.Rounds) != 3 || got.State() != "stopped_budget" {
		t.Fatal("restart reset budget", got.State())
	}
	roundInput.RequestID = "repair-round-4"
	if _, err := reopened.ClaimSkillHubPublicationRepairRound(ctx, actor, id, roundInput, "worker-3"); !errors.Is(err, store.ErrSkillHubPublicationExhausted) {
		t.Fatal("fourth repair round accepted", err)
	}
	if got := createRepairBudget(t, reopened, actor, input); len(got.Rounds) != 3 {
		t.Fatal("start replay reset exhausted budget")
	}
	if _, err := db.db.Exec(repairBudgetMigration(t, "down")); err == nil {
		t.Fatal("rollback erased budget")
	}
}

func TestPostgresSkillHubPublicationRepairStopsAndEvidence(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"no_progress", "waiting_confirmation", "validated", "stopped"} {
		t.Run(outcome, func(t *testing.T) {
			db, _, in, input, _, _ := repairBudgetFixture(t)
			actor, id := in.Binding.ActorUID, in.EntryID
			createRepairBudget(t, db, actor, input)
			ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
			wrong := ri
			wrong.Evidence.ReportHash = strings.Repeat("f", 64)
			if _, err := db.ClaimSkillHubPublicationRepairRound(context.Background(), actor, id, wrong, "worker-1"); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal("stale first report accepted", err)
			}
			r := claimRepairBudget(t, db, actor, id, ri, "worker-1")
			if _, err := db.ClaimSkillHubPublicationRepairRound(context.Background(), actor, id, wrong, "worker-1"); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal("request key rebound", err)
			}
			result := store.SkillHubPublicationRepairResult{Outcome: outcome, Evidence: input.Evidence}
			if outcome == "no_progress" {
				result.Outcome = "rejected"
				result.Evidence.ReportHash = strings.Repeat("f", 64)
			}
			if outcome == "stopped" {
				result.Code = "model_result_uncertain"
			}
			finishRepairBudget(t, db, actor, r.Lease, result)
			ri.RequestID = "repair-round-2"
			ri.Evidence = result.Evidence
			if _, err := db.ClaimSkillHubPublicationRepairRound(context.Background(), actor, id, ri, "worker-1"); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatal("terminal or unconfirmed repair continued", err)
			}
			expected := outcome
			if outcome == "no_progress" {
				expected = "stopped_no_progress"
			}
			if got := getRepairBudget(t, db, actor, id); got.State() != expected {
				t.Fatal("wrong stop state", got.State())
			}
		})
	}
}

func TestPostgresSkillHubPublicationRepairAuthorityAndPrivateHistory(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "transfer", "human_disabled", "bot_disabled", "bot_account_disabled", "body", "commit"} {
		t.Run(change, func(t *testing.T) {
			db, _, in, input, c, other := repairBudgetFixture(t)
			actor, id, ctx := in.Binding.ActorUID, in.EntryID, context.Background()
			if _, err := db.CreateSkillHubPublicationRepairTask(ctx, other, input); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
				t.Fatal("other actor created task", err)
			}
			createRepairBudget(t, db, actor, input)
			ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
			r := claimRepairBudget(t, db, actor, id, ri, "worker-1")
			switch change {
			case "cancel":
				requestCancelIntent(t, db, actor, id, "ready")
			case "transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "human_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
			case "bot_disabled":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
			case "bot_account_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.BotUID)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='other-body' WHERE user_id=$1`, in.Binding.BotUID)
			case "commit":
				recordCommit(t, db, actor, c)
			}
			if _, err := db.CreateSkillHubPublicationRepairTask(ctx, actor, input); err == nil {
				t.Fatal("task renewed after authority changed")
			}
			if _, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, "worker-1"); err == nil {
				t.Fatal("claim replay renewed authority")
			}
			if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, r.Lease, store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: input.Evidence}); err == nil {
				t.Fatal("checkpoint after authority changed")
			}
			got, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id)
			if err != nil || (got == nil) != (change == "human_disabled") {
				t.Fatal("private original publisher history", err)
			}
			for _, reader := range []int64{other, in.Binding.BotUID} {
				if got, err := db.GetSkillHubPublicationRepairTask(ctx, reader, id); err != nil || got != nil {
					t.Fatal("private history leaked", err)
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationRepairConcurrentClaimsAndFencing(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, _ := repairBudgetFixture(t)
	actor, id, ctx := in.Binding.ActorUID, in.EntryID, context.Background()
	createRepairBudget(t, db, actor, input)
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := db
			if i%2 == 0 {
				target = peer
			}
			_, err := target.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, fmt.Sprintf("worker-%d", i))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, store.ErrSkillHubPublicationBusy) {
			t.Fatal(err)
		}
	}
	if won != 1 || len(getRepairBudget(t, db, actor, id).Rounds) != 1 {
		t.Fatal("concurrent claim spent extra rounds", won)
	}
	expireRepairBudget(t, db, id)
	expired := getRepairBudget(t, db, actor, id).Rounds[0].Lease
	if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, expired, store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: input.Evidence}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("expired lease finished without takeover", err)
	}
	// A different request ID must not silently allocate the next round after a
	// crash. Recovery must inspect/resume the reserved round's request identity.
	changed := ri
	changed.RequestID = "another-round-1"
	if _, err := peer.ClaimSkillHubPublicationRepairRound(ctx, actor, id, changed, "recovery"); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("expired reservation spent next round", err)
	}
	r := claimRepairBudget(t, peer, actor, id, ri, "recovery")
	wrong := r.Lease
	wrong.Token = strings.Repeat("0", 32)
	if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, wrong, store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: input.Evidence}); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("forged lease accepted", err)
	}
}

func TestPostgresSkillHubPublicationRepairDatabaseGuards(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	actor, id := in.Binding.ActorUID, in.EntryID
	createRepairBudget(t, db, actor, input)
	ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
	r := claimRepairBudget(t, db, actor, id, ri, "worker-1")
	for _, q := range []string{
		`DELETE FROM skillhub_publication_repair_tasks WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_tasks SET task_id='pubrepair_00000000000000000000000000000000' WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_repair_rounds WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_rounds SET lease_generation=1,lease_token=repeat('0',32) WHERE entry_id=$1`,
		`UPDATE skillhub_publication_repair_rounds SET request_id='replacement-request' WHERE entry_id=$1`,
		`INSERT INTO skillhub_publication_repair_rounds SELECT entry_id,3,'repair-skipped-3',jsonb_set(input,'{requestId}','"repair-skipped-3"'),identity_hash,lease_worker,lease_token,1,lease_until,NULL FROM skillhub_publication_repair_rounds WHERE entry_id=$1`,
		`INSERT INTO skillhub_publication_repair_rounds SELECT entry_id,4,'repair-budget-4',jsonb_set(input,'{requestId}','"repair-budget-4"'),identity_hash,lease_worker,lease_token,1,lease_until,NULL FROM skillhub_publication_repair_rounds WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(q, id); err == nil {
			t.Fatal("database reset retained evidence", q)
		}
	}
	finishRepairBudget(t, db, actor, r.Lease, store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: input.Evidence})
	if _, err := db.db.Exec(`UPDATE skillhub_publication_repair_rounds SET result=NULL WHERE entry_id=$1`, id); err == nil {
		t.Fatal("database erased checkpoint")
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got := getRepairBudget(t, db, actor, id); len(got.Rounds) != 1 || got.State() != "waiting_confirmation" {
		t.Fatal("startup reset checkpoint")
	}
}

func TestPostgresSkillHubPublicationRepairCancellationRace(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"claim", "finish"} {
		t.Run(operation, func(t *testing.T) {
			db, _, in, input, _, _ := repairBudgetFixture(t)
			ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
			createRepairBudget(t, db, actor, input)
			ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
			var lease store.SkillHubPublicationRepairLease
			if operation == "finish" {
				lease = claimRepairBudget(t, db, actor, id, ri, "worker-1").Lease
			}
			result := store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: input.Evidence}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var repairErr, cancelErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				if operation == "claim" {
					_, repairErr = db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, "worker-1")
				} else {
					repairErr = db.FinishSkillHubPublicationRepairRound(ctx, actor, lease, result)
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				_, cancelErr = db.RequestSkillHubPublicationCancellation(ctx, actor, id)
			}()
			close(start)
			wg.Wait()
			if cancelErr != nil || (repairErr != nil && !errors.Is(repairErr, store.ErrSkillHubPublicationSettled)) {
				t.Fatalf("cancel=%v repair=%v", cancelErr, repairErr)
			}
			task := getRepairBudget(t, db, actor, id)
			if operation == "claim" {
				if (len(task.Rounds) == 1) != (repairErr == nil) {
					t.Fatal("claim result disagrees with retained budget")
				}
			} else if (task.Rounds[0].Result != nil) != (repairErr == nil) {
				t.Fatal("finish result disagrees with retained checkpoint")
			}
			if _, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, "worker-1"); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatal("claim replay bypassed completed cancellation", err)
			}
			if operation == "finish" {
				if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, lease, result); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
					t.Fatal("finish replay bypassed completed cancellation", err)
				}
			}
		})
	}
}
