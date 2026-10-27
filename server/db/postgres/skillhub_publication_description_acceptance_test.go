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

func descriptionAcceptanceMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000055_skillhub_publication_description_acceptance." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationDescriptionAcceptanceMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(descriptionAcceptanceMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionAcceptanceTable), " ") {
		t.Fatal("000055 differs from startup")
	}
}

// Real PostgreSQL persistence/authority; candidate metadata and the trusted peer
// report are synthetic. This does not exercise a live runtime or paid model.
func acceptanceApplication(t *testing.T, db *Adapter, actor int64, input store.SkillHubPublicationRepairInput) *store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	confirmedApplicationDraft(t, db, actor, input)
	app := prepareApplication(t, db, actor, input.Receipt.Query.EntryID)
	app, err := db.CompleteSkillHubPublicationDescriptionApplication(context.Background(), actor, *app, applicationResult(*app))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func acceptanceReport(app store.SkillHubPublicationDescriptionApplication) store.PublicationValidationReport {
	return store.PublicationValidationReport{SchemaVersion: "1", RuleVersion: "skillhub-publish-v1.0.0", Profile: "skillhub-publish-v1",
		ReportID: "12345678-1234-4123-8123-123456789abc", Outcome: "passed", Coverage: "complete", CandidateHash: app.Result.Derivation.DerivedCandidateHash,
		LimitsFingerprint: app.Result.Derivation.LimitsFingerprint, Issues: []store.PublicationValidationIssue{}, NextAction: "none"}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceRecoveryAndReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, other := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	app := acceptanceApplication(t, db, actor, input)
	report := acceptanceReport(*app)
	if got, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, *app); err != nil || got != nil {
		t.Fatal("read wrote acceptance", err)
	}
	accepted, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, report)
	if err != nil || accepted == nil || accepted.ValidateFor(*app) != nil {
		t.Fatal("accept", err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got, err := peer.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, *app); err != nil || !reflect.DeepEqual(got, accepted) {
		t.Fatal("restart lost acceptance", err)
	}
	if got, err := peer.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, report); err != nil || !reflect.DeepEqual(got, accepted) {
		t.Fatal("lost reply replay", err)
	}
	for _, uid := range []int64{other, in.Binding.BotUID} {
		if _, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, uid, *app); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign read", err)
		}
		if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, uid, *app, report); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign write", err)
		}
	}
	report.ReportID = "22345678-1234-4123-8123-123456789abc"
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, report); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("report replaced", err)
	}
	changed := *app
	result := *app.Result
	result.RequestBytes++
	changed.Result = &result
	if _, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("different application accepted", err)
	}
	task := getRepairBudget(t, db, actor, id)
	if len(task.Rounds) != 1 || task.State() != "waiting_confirmation" {
		t.Fatal("acceptance rewrote round/budget")
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("acceptance published", err)
	}
	if _, err := db.db.Exec(descriptionAcceptanceMigration(t, "down")); err == nil {
		t.Fatal("down erased retained report")
	}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceDatabaseGuards(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	actor := in.Binding.ActorUID
	app := acceptanceApplication(t, db, actor, input)
	r := acceptanceReport(*app)
	for _, expression := range []string{
		`$3::jsonb-'candidateHash'`,
		`jsonb_set($3::jsonb,'{candidateHash}',to_jsonb(repeat('f',64)))`,
		`jsonb_set($3::jsonb,'{limitsFingerprint}',to_jsonb(repeat('f',64)))`,
		`jsonb_set($3::jsonb,'{reportId}','"invalid"')`,
		`jsonb_set($3::jsonb,'{ruleVersion}','"invalid"')`,
		`jsonb_set($3::jsonb,'{outcome}','"rejected"')`,
		`jsonb_set($3::jsonb,'{coverage}','"partial"')`,
		`jsonb_set($3::jsonb,'{truncated}','true')`,
		`jsonb_set($3::jsonb,'{nextAction}','"review_report"')`,
		`jsonb_set($3::jsonb,'{issues}','null')`,
		`jsonb_set($3::jsonb,'{issues}','[{"blocking":true}]')`,
	} {
		_, err := db.db.Exec(`INSERT INTO skillhub_publication_description_acceptances(application_id,result,report,identity_hash) VALUES($1,$2,`+expression+`,repeat('a',64))`, app.ID, applicationJSON(t, *app.Result), applicationJSON(t, r))
		if err == nil {
			t.Fatal("unsafe SQL report accepted", expression)
		}
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_acceptances(application_id,result,report,identity_hash) VALUES($1,jsonb_set($2::jsonb,'{requestBytes}','101'),$3,repeat('a',64))`, app.ID, applicationJSON(t, *app.Result), applicationJSON(t, r)); err == nil {
		t.Fatal("SQL result replaced")
	}
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(context.Background(), actor, *app, r); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE skillhub_publication_description_acceptances SET accepted_at=clock_timestamp() WHERE application_id=$1`,
		`UPDATE skillhub_publication_description_acceptances SET report=jsonb_set(report,'{reportId}','"22345678-1234-4123-8123-123456789abc"') WHERE application_id=$1`,
		`DELETE FROM skillhub_publication_description_acceptances WHERE application_id=$1`,
	} {
		if _, err := db.db.Exec(q, app.ID); err == nil {
			t.Fatal("history mutated", q)
		}
	}
	// Corrupt evidence cannot be recovered as valid merely because its row exists.
	deliveryExec(t, db, `ALTER TABLE skillhub_publication_description_acceptances DISABLE TRIGGER guard_skillhub_publication_description_acceptance`)
	deliveryExec(t, db, `UPDATE skillhub_publication_description_acceptances SET identity_hash=repeat('f',64) WHERE application_id=$1`, app.ID)
	deliveryExec(t, db, `ALTER TABLE skillhub_publication_description_acceptances ENABLE TRIGGER guard_skillhub_publication_description_acceptance`)
	if _, err := db.GetSkillHubPublicationDescriptionAcceptance(context.Background(), actor, *app); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("corrupt evidence recovered", err)
	}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceRequiresCompletedApplication(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	confirmedApplicationDraft(t, db, in.Binding.ActorUID, input)
	app := prepareApplication(t, db, in.Binding.ActorUID, in.EntryID)
	result := applicationResult(*app)
	pretend := *app
	pretend.Result = &result
	r := acceptanceReport(pretend)
	ctx := context.Background()
	if _, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, in.Binding.ActorUID, *app); err == nil {
		t.Fatal("incomplete application read")
	}
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, in.Binding.ActorUID, *app, r); err == nil {
		t.Fatal("incomplete application accepted")
	}
	if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_acceptances(application_id,result,report,identity_hash) VALUES($1,$2,$3,repeat('a',64))`,
		app.ID, applicationJSON(t, result), applicationJSON(t, r)); err == nil {
		t.Fatal("SQL bypassed incomplete application")
	}
	// An empty migration can be rolled down/up without changing the existing
	// application or confirmation evidence.
	if _, err := db.db.Exec(descriptionAcceptanceMigration(t, "down")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(descriptionAcceptanceMigration(t, "up")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(descriptionAcceptanceMigration(t, "up")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "transfer", "human_disabled", "bot_disabled", "body", "commit", "group_kind", "group_owner", "extra_member", "missing_bot", "topic_owner"} {
		t.Run(change, func(t *testing.T) {
			db, _, in, input, c, other := repairBudgetFixture(t)
			ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
			app := acceptanceApplication(t, db, actor, input)
			r := acceptanceReport(*app)
			// Test both initial insertion and recovery after authority loss.
			if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, r); err != nil {
				t.Fatal(err)
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
			if _, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, *app); err == nil {
				t.Fatal("changed authority read")
			}
			if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, r); err == nil {
				t.Fatal("changed authority replay")
			}
			// Remove only in this isolated test schema, bypassing the history guard,
			// to exercise direct SQL insertion under the now-invalid authority.
			deliveryExec(t, db, `ALTER TABLE skillhub_publication_description_acceptances DISABLE TRIGGER guard_skillhub_publication_description_acceptance`)
			deliveryExec(t, db, `DELETE FROM skillhub_publication_description_acceptances WHERE application_id=$1`, app.ID)
			deliveryExec(t, db, `ALTER TABLE skillhub_publication_description_acceptances ENABLE TRIGGER guard_skillhub_publication_description_acceptance`)
			if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, r); err == nil {
				t.Fatal("changed authority inserted")
			}
			if _, err := db.db.Exec(`INSERT INTO skillhub_publication_description_acceptances(application_id,result,report,identity_hash) VALUES($1,$2,$3,repeat('a',64))`, app.ID, applicationJSON(t, *app.Result), applicationJSON(t, r)); err == nil {
				t.Fatal("SQL bypassed authority")
			}
		})
	}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceConcurrentRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	app := acceptanceApplication(t, db, actor, input)
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	start := make(chan struct{})
	results := make(chan *store.SkillHubPublicationDescriptionAcceptance, 8)
	errs := make(chan error, 8)
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
			r := acceptanceReport(*app)
			if i%2 == 1 {
				r.ReportID = "22345678-1234-4123-8123-123456789abc"
			}
			got, err := target.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, r)
			if errors.Is(err, store.ErrSkillHubPublicationConflict) {
				got, err = target.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, *app)
			}
			errs <- err
			results <- got
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *store.SkillHubPublicationDescriptionAcceptance
	for a := range results {
		if a == nil {
			t.Fatal("missing winner")
		}
		if first == nil {
			first = a
		}
		if !reflect.DeepEqual(first, a) {
			t.Fatal("concurrent reports diverged")
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_acceptances WHERE application_id=$1`, app.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate evidence", count, err)
	}
}

func TestPostgresSkillHubPublicationDescriptionAcceptanceCancellationRace(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	app := acceptanceApplication(t, db, actor, input)
	r := acceptanceReport(*app)
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		_, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, r)
		done <- err
	}()
	close(start)
	requestCancelIntent(t, db, actor, id, "ready")
	if err := <-done; err != nil && !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal(err)
	}
	if _, err := db.GetSkillHubPublicationDescriptionAcceptance(ctx, actor, *app); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("cancelled replay", err)
	}
}
