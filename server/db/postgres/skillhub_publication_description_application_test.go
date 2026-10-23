package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

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

func confirmedApplicationDraft(t *testing.T, db *Adapter, actor int64, input store.SkillHubPublicationRepairInput) *store.SkillHubPublicationDescriptionDraft {
	t.Helper()
	createRepairBudget(t, db, actor, input)
	r := claimRepairBudget(t, db, actor, input.Receipt.Query.EntryID, store.SkillHubPublicationRepairRoundInput{RequestID: "application-round-1", Evidence: input.Evidence}, "application-worker")
	d := proposeDescription(t, db, actor, r.Lease, descriptionProposal())
	d, err := db.ConfirmSkillHubPublicationDescription(context.Background(), actor, d.EntryID, d.ConfirmationKey)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Synthetic derivation metadata only: these tests exercise the database
// contract, not a real retained Cloud candidate or a model/file repair E2E.
func applicationResult(app store.SkillHubPublicationDescriptionApplication) store.SkillHubPublicationDescriptionApplicationResult {
	return store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: 100,
		Derivation: store.SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: strings.Repeat("6", 64), LimitsFingerprint: strings.Repeat("7", 64),
			DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
}

func prepareApplication(t *testing.T, db *Adapter, actor int64, id string) *store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	a, err := db.PrepareSkillHubPublicationDescriptionApplication(context.Background(), actor, id)
	if err != nil || a == nil {
		t.Fatal("prepare application", err)
	}
	return a
}

func applicationJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPostgresSkillHubPublicationDescriptionApplicationRecoveryAndReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, other := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	if _, err := db.PrepareSkillHubPublicationDescriptionApplication(ctx, actor, id); err == nil {
		t.Fatal("missing draft accepted")
	}
	d := confirmedApplicationDraft(t, db, actor, input)
	if got, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id); err != nil || got != nil {
		t.Fatal("read created application", err)
	}
	a := prepareApplication(t, db, actor, id)
	if !reflect.DeepEqual(a, prepareApplication(t, db, actor, id)) {
		t.Fatal("prepare replay changed identity")
	}
	for _, uid := range []int64{other, in.Binding.BotUID} {
		if _, err := db.PrepareSkillHubPublicationDescriptionApplication(ctx, uid, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign prepare", err)
		}
		if _, err := db.GetSkillHubPublicationDescriptionApplication(ctx, uid, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign read", err)
		}
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got := prepareApplication(t, peer, actor, id); !reflect.DeepEqual(got, a) {
		t.Fatal("restart lost application")
	}
	result := applicationResult(*a)
	completed, err := peer.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *a, result)
	if err != nil || completed == nil || completed.AppliedAt == nil || completed.Result == nil || *completed.Result != result {
		t.Fatal("complete", err)
	}
	if got, err := db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *a, result); err != nil || !reflect.DeepEqual(got, completed) {
		t.Fatal("lost-reply replay", err)
	}
	if got, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id); err != nil || !reflect.DeepEqual(got, completed) {
		t.Fatal("read retained result", err)
	}
	changed := result
	changed.RequestBytes++
	if err := changed.ValidateFor(*a); err != nil {
		t.Fatal("replacement fixture must be valid before testing retained-result conflict", err)
	}
	if _, err := db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *a, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("result replaced", err)
	}
	task := getRepairBudget(t, db, actor, id)
	if len(task.Rounds) != 1 || task.State() != "waiting_confirmation" || a.ValidateFor(*task, *d) != nil {
		t.Fatal("application changed budget/confirmation")
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("application published", err)
	}
	if _, err := db.db.Exec(descriptionApplicationMigration(t, "down")); err == nil {
		t.Fatal("down erased retained application")
	}
}

func TestPostgresSkillHubPublicationDescriptionApplicationDatabaseGuards(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	actor, id := in.Binding.ActorUID, in.EntryID
	d := confirmedApplicationDraft(t, db, actor, input)
	ai := store.SkillHubPublicationDescriptionApplicationInput{Receipt: input.Receipt, Draft: d.Input, ConfirmationKey: d.ConfirmationKey}
	h, err := ai.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	a := store.SkillHubPublicationDescriptionApplication{ID: "pubdesc_" + h, Input: ai, IdentityHash: h}
	insert := `INSERT INTO skillhub_publication_description_applications(entry_id,round_number,confirmation_key,application_id,input,identity_hash,result,applied_at)
		VALUES($1,1,$2,$3,$4,$5,$6,CASE WHEN $6::jsonb IS NULL THEN NULL ELSE clock_timestamp() END)`
	for _, tc := range []struct {
		name   string
		mutate func(*store.SkillHubPublicationDescriptionApplication)
		result bool
	}{
		{"completed_insert", func(*store.SkillHubPublicationDescriptionApplication) {}, true},
		{"application_id", func(a *store.SkillHubPublicationDescriptionApplication) { a.ID = "pubdesc_" + strings.Repeat("f", 64) }, false},
		{"identity_hash", func(a *store.SkillHubPublicationDescriptionApplication) { a.IdentityHash = strings.Repeat("f", 64) }, false},
		{"foreign_receipt", func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Input.Receipt.Query.RequestHash = strings.Repeat("f", 64)
		}, false},
		{"cross_round_draft", func(a *store.SkillHubPublicationDescriptionApplication) { a.Input.Draft.Round = 2 }, false},
		{"confirmation_key", func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Input.ConfirmationKey = strings.Repeat("f", 64)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := a
			tc.mutate(&bad)
			var result any
			if tc.result {
				result = applicationJSON(t, applicationResult(a))
			}
			if _, err := db.db.Exec(insert, id, d.ConfirmationKey, bad.ID, applicationJSON(t, bad.Input), bad.IdentityHash, result); err == nil {
				t.Fatal("forged insert accepted")
			}
		})
	}
	app := prepareApplication(t, db, actor, id)
	valid := applicationResult(*app)
	for _, tc := range []struct{ name, expression string }{
		{"wrong_id", `jsonb_set($2::jsonb,'{applicationId}','"pubdesc_wrong"')`},
		{"missing_bytes", `$2::jsonb-'requestBytes'`},
		{"string_bytes", `jsonb_set($2::jsonb,'{requestBytes}','"abc"')`},
		{"fractional_bytes", `jsonb_set($2::jsonb,'{requestBytes}','1.5')`},
		{"large_bytes", `jsonb_set($2::jsonb,'{requestBytes}','78643201')`},
		{"schema", `jsonb_set($2::jsonb,'{derivation,schemaVersion}','"2"')`},
		{"algorithm", `jsonb_set($2::jsonb,'{derivation,algorithm}','"other"')`},
		{"request_binding", `jsonb_set($2::jsonb,'{derivation,originalRequestHash}',to_jsonb(repeat('f',64)))`},
		{"candidate_binding", `jsonb_set($2::jsonb,'{derivation,originalCandidateHash}',to_jsonb(repeat('f',64)))`},
		{"noop_request", `jsonb_set($2::jsonb,'{derivation,derivedRequestHash}',$2::jsonb->'derivation'->'originalRequestHash')`},
		{"noop_candidate", `jsonb_set($2::jsonb,'{derivation,derivedCandidateHash}',$2::jsonb->'derivation'->'originalCandidateHash')`},
		{"invalid_digest", `jsonb_set($2::jsonb,'{derivation,proposalDigest}','"bad"')`},
		{"missing_digest", `$2::jsonb #- '{derivation,limitsFingerprint}'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.db.Exec(`UPDATE skillhub_publication_description_applications SET result=`+tc.expression+`,applied_at=clock_timestamp() WHERE entry_id=$1`, id, applicationJSON(t, valid))
			if err == nil {
				t.Fatal("invalid SQL completion accepted")
			}
			if strings.Contains(err.Error(), "invalid input syntax") {
				t.Fatal("raw numeric cast escaped guard", err)
			}
		})
	}
	if _, err := db.CompleteSkillHubPublicationDescriptionApplication(context.Background(), actor, *app, valid); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE skillhub_publication_description_applications SET applied_at=clock_timestamp() WHERE entry_id=$1`,
		`UPDATE skillhub_publication_description_applications SET result=NULL,applied_at=NULL WHERE entry_id=$1`,
		`UPDATE skillhub_publication_description_applications SET identity_hash=repeat('f',64) WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_description_applications WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(q, id); err == nil {
			t.Fatal("completed row mutated", q)
		}
	}
}

func TestPostgresSkillHubPublicationDescriptionApplicationAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "transfer", "human_disabled", "bot_disabled", "body", "commit", "group_kind", "group_owner", "extra_member", "missing_bot", "topic_owner"} {
		t.Run(change, func(t *testing.T) {
			db, _, in, input, c, other := repairBudgetFixture(t)
			ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
			confirmedApplicationDraft(t, db, actor, input)
			app := prepareApplication(t, db, actor, id)
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
			if _, err := db.PrepareSkillHubPublicationDescriptionApplication(ctx, actor, id); err == nil {
				t.Fatal("changed authority prepared/replayed")
			}
			if _, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id); err == nil {
				t.Fatal("changed authority read")
			}
			result := applicationResult(*app)
			if _, err := db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *app, result); err == nil {
				t.Fatal("changed authority completed")
			}
			if _, err := db.db.Exec(`UPDATE skillhub_publication_description_applications SET result=$2,applied_at=clock_timestamp() WHERE entry_id=$1`, id, applicationJSON(t, result)); err == nil {
				t.Fatal("SQL bypassed changed authority")
			}
		})
	}
}

func TestPostgresSkillHubPublicationDescriptionApplicationConcurrentReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	confirmedApplicationDraft(t, db, actor, input)
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	start := make(chan struct{})
	results := make(chan *store.SkillHubPublicationDescriptionApplication, 8)
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
			a, err := target.PrepareSkillHubPublicationDescriptionApplication(ctx, actor, id)
			if err == nil {
				a, err = target.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *a, applicationResult(*a))
			}
			errs <- err
			results <- a
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
	var first *store.SkillHubPublicationDescriptionApplication
	for a := range results {
		if first == nil {
			first = a
		}
		if !reflect.DeepEqual(a, first) {
			t.Fatal("concurrent application diverged")
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_description_applications WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate applications", count, err)
	}
}

func TestPostgresSkillHubPublicationDescriptionApplicationCancellationRace(t *testing.T) {
	t.Parallel()
	db, _, in, input, _, _ := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	confirmedApplicationDraft(t, db, actor, input)
	app := prepareApplication(t, db, actor, id)
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		_, err := db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *app, applicationResult(*app))
		done <- err
	}()
	close(start)
	requestCancelIntent(t, db, actor, id, "ready")
	if err := <-done; err != nil && !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("unexpected race result", err)
	}
	if _, err := db.CompleteSkillHubPublicationDescriptionApplication(ctx, actor, *app, applicationResult(*app)); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("replay bypassed cancellation", err)
	}
}
