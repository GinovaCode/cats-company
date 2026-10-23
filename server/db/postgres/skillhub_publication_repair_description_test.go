package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func descriptionMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000052_skillhub_publication_description_confirmation." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func descriptionApplicationMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000053_skillhub_publication_description_application." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestSkillHubPublicationDescriptionApplicationMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(descriptionApplicationMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionApplicationTable), " ") {
		t.Fatal("000053 differs from startup")
	}
}
func TestSkillHubPublicationDescriptionMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(descriptionMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionTables), " ") {
		t.Fatal("000052 differs from startup")
	}
}
func descriptionProposal() store.SkillHubPublicationDescriptionProposal {
	i := int64(0)
	return store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &i, Text: "Read a document and return its contents."}
}
func proposeDescription(t *testing.T, db *Adapter, actor int64, l store.SkillHubPublicationRepairLease, p store.SkillHubPublicationDescriptionProposal) *store.SkillHubPublicationDescriptionDraft {
	t.Helper()
	d, err := db.ProposeSkillHubPublicationDescription(context.Background(), actor, l, p)
	if err != nil || d == nil {
		t.Fatal("propose description", err)
	}
	return d
}
func TestPostgresSkillHubPublicationDescriptionRecoveryAndReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, other := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	createRepairBudget(t, db, actor, input)
	ri := store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}
	r := claimRepairBudget(t, db, actor, id, ri, "description-worker")
	if d, err := db.GetSkillHubPublicationDescription(ctx, actor, id); err != nil || d != nil {
		t.Fatal("read invented a draft", err)
	}
	d := proposeDescription(t, db, actor, r.Lease, descriptionProposal())
	task := getRepairBudget(t, db, actor, id)
	if d.ValidateFor(*task) != nil || len(task.Rounds) != 1 || task.State() != "waiting_confirmation" || d.ConfirmedAt != nil {
		t.Fatal("proposal did not atomically stop at confirmation")
	}
	if _, err := db.ConfirmSkillHubPublicationDescription(ctx, actor, id, strings.Repeat("f", 64)); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("wrong draft key confirmed", err)
	}
	for _, uid := range []int64{other, in.Binding.BotUID} {
		if _, err := db.GetSkillHubPublicationDescription(ctx, uid, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("private draft leaked", err)
		}
		if _, err := db.ConfirmSkillHubPublicationDescription(ctx, uid, id, d.ConfirmationKey); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("nonhuman/other owner confirmed", err)
		}
	}
	changed := descriptionProposal()
	changed.Text += " changed"
	if _, err := db.ProposeSkillHubPublicationDescription(ctx, actor, r.Lease, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("proposal overwritten", err)
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetSkillHubPublicationDescription(ctx, actor, id)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatal("draft lost on restart", err)
	}
	confirmed, err := reopened.ConfirmSkillHubPublicationDescription(ctx, actor, id, d.ConfirmationKey)
	if err != nil || confirmed.ConfirmedAt == nil {
		t.Fatal("exact confirmation", err)
	}
	replay, err := db.ConfirmSkillHubPublicationDescription(ctx, actor, id, d.ConfirmationKey)
	if err != nil || !reflect.DeepEqual(replay, confirmed) {
		t.Fatal("confirmation replay changed approval", err)
	}
	if got := proposeDescription(t, db, actor, r.Lease, descriptionProposal()); !reflect.DeepEqual(got, confirmed) {
		t.Fatal("proposal replay changed confirmed draft")
	}
	ri.RequestID = "description-round-2"
	if _, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, "new-worker"); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("confirmation unlocked a new round", err)
	}
	task = getRepairBudget(t, db, actor, id)
	if len(task.Rounds) != 1 || task.State() != "waiting_confirmation" {
		t.Fatal("confirmation reset or spent budget")
	}
	var n int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("confirmation published", err)
	}
	if _, err := db.db.Exec(descriptionMigration(t, "down")); err == nil {
		t.Fatal("down migration erased draft and confirmation")
	}
}
func TestPostgresSkillHubPublicationDescriptionFencingAndAtomic(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	createRepairBudget(t, db, actor, input)
	ri := store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}
	old := claimRepairBudget(t, db, actor, id, ri, "old-worker").Lease
	expireRepairBudget(t, db, id)
	live := claimRepairBudget(t, db, actor, id, ri, "new-worker").Lease
	if _, err := db.ProposeSkillHubPublicationDescription(ctx, actor, old, descriptionProposal()); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("stale worker retained draft", err)
	}
	// Force the second write to fail: neither the draft nor waiting checkpoint
	// may survive independently of the other.
	deliveryExec(t, db, `CREATE FUNCTION fail_description_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.result IS NOT NULL THEN RAISE EXCEPTION 'injected description failure'; END IF; RETURN NEW; END; $$;
		CREATE TRIGGER zz_fail_description_checkpoint BEFORE UPDATE ON skillhub_publication_repair_rounds FOR EACH ROW EXECUTE FUNCTION fail_description_checkpoint()`)
	if _, err := db.ProposeSkillHubPublicationDescription(ctx, actor, live, descriptionProposal()); err == nil {
		t.Fatal("injected failure ignored")
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_drafts WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial draft retained", err)
	}
	if task := getRepairBudget(t, db, actor, id); task.Rounds[0].Result != nil {
		t.Fatal("partial waiting checkpoint retained")
	}
	deliveryExec(t, db, `DROP TRIGGER zz_fail_description_checkpoint ON skillhub_publication_repair_rounds; DROP FUNCTION fail_description_checkpoint()`)
	proposeDescription(t, db, actor, live, descriptionProposal())
}
func TestPostgresSkillHubPublicationDescriptionAuthority(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"proposal", "confirmation"} {
		for _, change := range []string{"cancel", "transfer", "human_disabled", "bot_disabled", "body", "commit", "group_kind", "group_owner", "extra_member", "missing_bot", "topic_owner"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				db, _, in, input, c, other := repairBudgetFixture(t)
				ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
				createRepairBudget(t, db, actor, input)
				r := claimRepairBudget(t, db, actor, id, store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}, "worker-1")
				var d *store.SkillHubPublicationDescriptionDraft
				if stage == "confirmation" {
					d = proposeDescription(t, db, actor, r.Lease, descriptionProposal())
				}
				group, _ := store.ParseSkillHubPublicationRepairTopic(input.TopicID)
				switch change {
				case "cancel":
					requestCancelIntent(t, db, actor, id, "ready")
				case "transfer":
					deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
				case "human_disabled":
					deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
				case "bot_disabled":
					deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
				case "body":
					deliveryExec(t, db, `UPDATE bot_config SET body_id='other-body' WHERE user_id=$1`, in.Binding.BotUID)
				case "commit":
					recordCommit(t, db, actor, c)
				case "group_kind":
					deliveryExec(t, db, `UPDATE "groups" SET group_kind='standard' WHERE id=$1`, group)
				case "group_owner":
					deliveryExec(t, db, `UPDATE "groups" SET owner_id=$1 WHERE id=$2`, other, group)
				case "extra_member":
					if err := db.AddGroupMember(group, other, "member"); err != nil {
						t.Fatal(err)
					}
				case "missing_bot":
					deliveryExec(t, db, `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, group, in.Binding.BotUID)
				case "topic_owner":
					deliveryExec(t, db, `UPDATE topics SET owner_id=$1 WHERE id=$2`, other, input.TopicID)
				}
				if _, err := db.ProposeSkillHubPublicationDescription(ctx, actor, r.Lease, descriptionProposal()); err == nil {
					t.Fatal("changed authority proposed/replayed")
				}
				if _, err := db.GetSkillHubPublicationDescription(ctx, actor, id); err == nil {
					t.Fatal("changed authority read draft")
				}
				if d != nil {
					if _, err := db.ConfirmSkillHubPublicationDescription(ctx, actor, id, d.ConfirmationKey); err == nil {
						t.Fatal("changed authority confirmed")
					}
				}
			})
		}
	}
}
func TestPostgresSkillHubPublicationDescriptionConcurrentConfirmation(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	createRepairBudget(t, db, actor, input)
	r := claimRepairBudget(t, db, actor, id, store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}, "worker-1")
	d := proposeDescription(t, db, actor, r.Lease, descriptionProposal())
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			target := db
			if i%2 == 1 {
				target = peer
			}
			got, err := target.ConfirmSkillHubPublicationDescription(ctx, actor, id, d.ConfirmationKey)
			if err == nil && (got == nil || got.ConfirmedAt == nil) {
				err = errors.New("confirmation missing")
			}
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_confirmations WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate approvals", count, err)
	}
}
func TestPostgresSkillHubPublicationDescriptionDatabaseGuards(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, other := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	createRepairBudget(t, db, actor, input)
	r := claimRepairBudget(t, db, actor, id, store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}, "worker-1")
	d := proposeDescription(t, db, actor, r.Lease, descriptionProposal())
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_confirmations(entry_id,round_number,confirmation_key,actor_uid) VALUES($1,1,$2,$3)`, id, d.ConfirmationKey, other); err == nil {
		t.Fatal("SQL wrong actor confirmed")
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_confirmations(entry_id,round_number,confirmation_key,actor_uid) VALUES($1,1,$2,$3)`, id, strings.Repeat("f", 64), actor); err == nil {
		t.Fatal("SQL wrong key confirmed")
	}
	if _, err := db.ConfirmSkillHubPublicationDescription(ctx, actor, id, d.ConfirmationKey); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE skillhub_publication_description_drafts SET input=jsonb_set(input,'{proposal,text}','"changed"') WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_description_drafts WHERE entry_id=$1`,
		`UPDATE skillhub_publication_description_confirmations SET confirmed_at=clock_timestamp() WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_description_confirmations WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(q, id); err == nil {
			t.Fatal("retained approval changed", q)
		}
	}
}
func TestPostgresSkillHubPublicationDescriptionCancellationRace(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"propose", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			db, _, in, input, _, _ := repairBudgetFixture(t)
			ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
			createRepairBudget(t, db, actor, input)
			r := claimRepairBudget(t, db, actor, id, store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: input.Evidence}, "worker-1")
			var draft *store.SkillHubPublicationDescriptionDraft
			if operation == "confirm" {
				draft = proposeDescription(t, db, actor, r.Lease, descriptionProposal())
			}
			start := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				<-start
				var err error
				if operation == "propose" {
					_, err = db.ProposeSkillHubPublicationDescription(ctx, actor, r.Lease, descriptionProposal())
				} else {
					_, err = db.ConfirmSkillHubPublicationDescription(ctx, actor, id, draft.ConfirmationKey)
				}
				done <- err
			}()
			close(start)
			requestCancelIntent(t, db, actor, id, "ready")
			if err := <-done; err != nil && !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatal("unexpected cancellation race", err)
			}
			if _, err := db.ProposeSkillHubPublicationDescription(ctx, actor, r.Lease, descriptionProposal()); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
				t.Fatal("proposal replay bypassed cancellation", err)
			}
			if draft != nil {
				if _, err := db.ConfirmSkillHubPublicationDescription(ctx, actor, id, draft.ConfirmationKey); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
					t.Fatal("confirmation replay bypassed cancellation", err)
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationDescriptionMigrationLifecycle(t *testing.T) {
	t.Parallel()
	db, _, _, _, _, _ := repairBudgetFixture(t)
	for _, q := range []string{descriptionApplicationMigration(t, "down"), descriptionMigration(t, "down"), descriptionMigration(t, "down"), descriptionMigration(t, "up"), descriptionMigration(t, "up"), descriptionApplicationMigration(t, "up")} {
		deliveryExec(t, db, q)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
}
