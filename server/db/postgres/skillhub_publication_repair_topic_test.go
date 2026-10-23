package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationRepairTopicAuthority(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"before_admission", "after_claim"} {
		for _, change := range []string{"ordinary_group", "foreign_owner", "extra_human", "missing_bot", "missing_owner", "owner_role", "foreign_topic_owner", "missing_topic", "deleted_group"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				db, _, in, input, _, other := repairBudgetFixture(t)
				actor, id, ctx := in.Binding.ActorUID, in.EntryID, context.Background()
				group, err := store.ParseSkillHubPublicationRepairTopic(input.TopicID)
				if err != nil {
					t.Fatal(err)
				}
				ri := store.SkillHubPublicationRepairRoundInput{RequestID: "repair-round-1", Evidence: input.Evidence}
				var lease store.SkillHubPublicationRepairLease
				if stage == "after_claim" {
					createRepairBudget(t, db, actor, input)
					lease = claimRepairBudget(t, db, actor, id, ri, "worker-1").Lease
				}
				switch change {
				case "ordinary_group":
					deliveryExec(t, db, `UPDATE "groups" SET group_kind='standard' WHERE id=$1`, group)
				case "foreign_owner":
					deliveryExec(t, db, `UPDATE "groups" SET owner_id=$1 WHERE id=$2`, other, group)
				case "extra_human":
					if err := db.AddGroupMember(group, other, "member"); err != nil {
						t.Fatal(err)
					}
				case "missing_bot":
					deliveryExec(t, db, `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, group, in.Binding.BotUID)
				case "missing_owner":
					deliveryExec(t, db, `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, group, actor)
				case "owner_role":
					deliveryExec(t, db, `UPDATE group_members SET role='member' WHERE group_id=$1 AND user_id=$2`, group, actor)
				case "foreign_topic_owner":
					deliveryExec(t, db, `UPDATE topics SET owner_id=$1 WHERE id=$2`, other, input.TopicID)
				case "missing_topic":
					deliveryExec(t, db, `DELETE FROM topics WHERE id=$1`, input.TopicID)
				case "deleted_group":
					deliveryExec(t, db, `DELETE FROM "groups" WHERE id=$1`, group)
				}
				if _, err := db.CreateSkillHubPublicationRepairTask(ctx, actor, input); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
					t.Fatal("invalid topic admitted/replayed", err)
				}
				if _, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, ri, "worker-1"); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
					t.Fatal("invalid topic claimed/replayed", err)
				}
				if stage == "before_admission" {
					if got, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id); err != nil || got != nil {
						t.Fatal("rejected topic left task", err)
					}
				} else {
					if err := db.FinishSkillHubPublicationRepairRound(ctx, actor, lease, store.SkillHubPublicationRepairResult{Outcome: "validated", Evidence: input.Evidence}); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
						t.Fatal("changed topic finished", err)
					}
					got := getRepairBudget(t, db, actor, id)
					if len(got.Rounds) != 1 || got.Rounds[0].Result != nil {
						t.Fatal("topic change reset budget or wrote result")
					}
				}
			})
		}
	}
}

func TestPostgresSkillHubPublicationRepairTopicMembershipFencing(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"insert", "remove", "change_kind"} {
		t.Run(operation, func(t *testing.T) {
			db, _, in, input, _, other := repairBudgetFixture(t)
			ctx := context.Background()
			tx, err := beginPublicationDelivery(ctx, db.db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			entry, err := lockPublicationRepairAuthority(ctx, tx, in.Binding.ActorUID, in.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			if err := lockPublicationRepairTopic(ctx, tx, entry, input.TopicID); err != nil {
				t.Fatal(err)
			}
			group, _ := store.ParseSkillHubPublicationRepairTopic(input.TopicID)
			conn, err := db.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `SET lock_timeout='150ms'`); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(ctx, `SET lock_timeout=0`)
			query := `INSERT INTO group_members(group_id,user_id,role) VALUES($1,$2,'member')`
			args := []any{group, other}
			switch operation {
			case "remove":
				query = `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`
				args = []any{group, in.Binding.BotUID}
			case "change_kind":
				query = `UPDATE "groups" SET group_kind='standard' WHERE id=$1`
				args = []any{group}
			}
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(bounded, query, args...); err == nil || !isRepairLockTimeout(err) {
				t.Fatal("topic mutation crossed admission lock", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(ctx, query, args...); err != nil {
				t.Fatal("topic mutation remained blocked after release", err)
			}
		})
	}
}

func isRepairLockTimeout(err error) bool {
	var sqlState interface{ SQLState() string }
	return errors.As(err, &sqlState) && sqlState.SQLState() == "55P03"
}
