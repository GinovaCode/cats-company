package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationDescriptionContinuationRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, input, _, other := repairBudgetFixture(t)
	ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
	app := acceptanceApplication(t, db, actor, input)
	if _, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("unaccepted application continued", err)
	}
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
		t.Fatal(err)
	}
	first, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
	if err != nil || first == nil || first.Validate() != nil {
		t.Fatal("continuation", err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	got, err := peer.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("reconstructed adapter changed continuation", err)
	}
	for _, uid := range []int64{other, in.Binding.BotUID} {
		if _, err := peer.GetSkillHubPublicationDescriptionContinuation(ctx, uid, id); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatal("foreign actor continued", err)
		}
	}
	if first.Task.State() != "waiting_confirmation" || len(first.Task.Rounds) != 1 {
		t.Fatal("continuation rewrote history/budget")
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("continuation published", count, err)
	}
	requestCancelIntent(t, db, actor, id, "ready")
	if _, err := peer.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("historical continuation bypassed cancellation", err)
	}
}

func TestPostgresSkillHubPublicationDescriptionContinuationAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"transfer", "human_disabled", "bot_disabled", "body", "member", "topic", "commit"} {
		t.Run(change, func(t *testing.T) {
			db, _, in, input, commit, other := repairBudgetFixture(t)
			ctx, actor, id := context.Background(), in.Binding.ActorUID, in.EntryID
			app := acceptanceApplication(t, db, actor, input)
			if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
				t.Fatal(err)
			}
			group, _ := store.ParseSkillHubPublicationRepairTopic(input.TopicID)
			switch change {
			case "transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "human_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
			case "bot_disabled":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='other-body' WHERE user_id=$1`, in.Binding.BotUID)
			case "member":
				if err := db.AddGroupMember(group, other, "member"); err != nil {
					t.Fatal(err)
				}
			case "topic":
				deliveryExec(t, db, `UPDATE topics SET owner_id=$1 WHERE id=$2`, other, input.TopicID)
			case "commit":
				recordCommit(t, db, actor, commit)
			}
			if _, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id); err == nil {
				t.Fatal("changed authority returned continuation")
			}
		})
	}
}
