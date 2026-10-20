package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func newSessionForkPostgres(t *testing.T) (*Adapter, types.SessionForkRequest) {
	t.Helper()
	dsn := os.Getenv("CATS_SESSION_FORK_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_SESSION_FORK_PG_TEST_DSN for isolated PostgreSQL fork integration")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("session_fork_test_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		base.Close()
		t.Fatal(err)
	}
	db := &Adapter{}
	t.Cleanup(func() { db.Close(); base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`); base.Close() })
	if err := db.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("startup idempotency: %v", err)
	}
	actor, err := db.CreateUser(&types.User{Username: "fork-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := db.CreateUser(&types.User{Username: "fork-agent", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec("INSERT INTO bot_config (user_id,owner_id,body_id) VALUES ($1,$2,'body-test') ON CONFLICT (user_id) DO UPDATE SET owner_id=$2,body_id='body-test'", agent, actor); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("p2p_%d_%d", actor, agent)
	if err = db.CreateTopic(source, "p2p", actor); err != nil {
		t.Fatal(err)
	}
	return db, types.SessionForkRequest{RequestID: "request-1", SourceTopicID: source, ActorUserID: actor, AgentUID: agent, BodyID: "body-test", Name: "Independent"}
}

func forkCreate(t *testing.T, db *Adapter, req types.SessionForkRequest) *types.SessionFork {
	t.Helper()
	f, err := db.CreateSessionFork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func forkComplete(t *testing.T, db *Adapter, f *types.SessionFork, state string) *types.SessionFork {
	t.Helper()
	got, err := db.CompleteSessionFork(context.Background(), f.AgentUID, types.SessionForkCompletion{BodyID: f.BodyID, RequestID: f.RequestID, TargetTopicID: f.TargetTopicID, State: state})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSessionForkPostgresFriendAndRevocation(t *testing.T) {
	db, req := newSessionForkPostgres(t)
	ctx := context.Background()
	if _, err := db.db.Exec("UPDATE bot_config SET owner_id=NULL WHERE user_id=$1", req.AgentUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateFriendRequest(req.ActorUserID, req.AgentUID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSessionFork(ctx, req); !errors.Is(err, store.ErrSessionForkForbidden) {
		t.Fatalf("pending friendship: %v", err)
	}
	if err := db.AcceptFriendRequest(req.ActorUserID, req.AgentUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "shared agent history", "text"); err != nil {
		t.Fatal(err)
	}
	f := forkCreate(t, db, req)
	if got := forkComplete(t, db, f, "ready"); got.State != "ready" {
		t.Fatalf("friend copy: %+v", got)
	}
	// A normal two-participant group is also a valid source, not just agent_task.
	if _, err := db.db.Exec("UPDATE groups SET group_kind='group' WHERE id=$1", strings.TrimPrefix(f.TargetTopicID, "grp_")); err != nil {
		t.Fatal(err)
	}
	req.RequestID, req.SourceTopicID = "friend-group", f.TargetTopicID
	second := forkCreate(t, db, req)
	if err := db.RemoveFriend(req.ActorUserID, req.AgentUID); err != nil {
		t.Fatal(err)
	}
	if got := forkComplete(t, db, second, "ready"); got.State != "failed" {
		t.Fatalf("revocation bypassed: %+v", got)
	}
	req.RequestID = "revoked"
	if _, err := db.CreateSessionFork(ctx, req); !errors.Is(err, store.ErrSessionForkForbidden) {
		t.Fatalf("revoked creation: %v", err)
	}
	if writable, err := db.SessionForkWritable(ctx, second.TargetTopicID); err != nil || writable {
		t.Fatalf("failed fork writable=%v err=%v", writable, err)
	}
}

func TestSessionForkPostgresCloneAndTerminalRetry(t *testing.T) {
	db, req := newSessionForkPostgres(t)
	blocks := []types.ContentBlock{{Type: "file", Payload: map[string]interface{}{"url": "/uploads/files/retained.pdf", "name": "report.pdf", "size": float64(12)}}}
	first, _, err := db.SaveMessageWithMetadata(req.SourceTopicID, req.ActorUserID, "visible", blocks, "normal", "user", "file", 0, "original-key", map[string]interface{}{"custom": "keep", "catsco_identity": map[string]interface{}{"token": "drop"}, "xiaoba_runtime": map[string]interface{}{"secret": "drop"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SaveMessageWithReply(req.SourceTopicID, req.AgentUID, "answer", "text", first)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2024, 2, 3, 4, 5, 6, 123456000, time.UTC)
	if _, err = db.db.Exec("UPDATE messages SET created_at=$1 WHERE topic_id=$2", stamp, req.SourceTopicID); err != nil {
		t.Fatal(err)
	}
	original, err := db.GetMessages(req.SourceTopicID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := forkCreate(t, db, req)
	if f.State != "pending" || f.SourceLastSeq != second || len(f.MessageIDMap) != 2 {
		t.Fatalf("fork %#v", f)
	}
	duplicate := forkCreate(t, db, req)
	if duplicate.TargetTopicID != f.TargetTopicID || !reflect.DeepEqual(duplicate.MessageIDMap, f.MessageIDMap) {
		t.Fatal("duplicate created new target/history")
	}
	changed := req
	changed.Name = "Different"
	if _, err = db.CreateSessionFork(context.Background(), changed); !errors.Is(err, store.ErrSessionForkConflict) {
		t.Fatalf("payload reuse %v", err)
	}
	copied, err := db.GetMessages(f.TargetTopicID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 2 || copied[0].ID == first || copied[1].ID == second || !copied[0].CreatedAt.Equal(stamp) || copied[0].Content != "visible" || !reflect.DeepEqual(copied[0].ContentBlocks, blocks) || copied[0].Metadata["custom"] != "keep" || copied[0].Metadata["catsco_identity"] != nil {
		t.Fatalf("copied %#v %#v", copied[0], copied[1])
	}
	if copied[1].ReplyTo != copied[0].ID {
		t.Fatalf("reply lost through ordinary history: %#v", copied[1])
	}
	// Ingress deduplication keys intentionally do not appear in history reads.
	var clientKey string
	if err = db.db.QueryRow("SELECT COALESCE(client_msg_id,'') FROM messages WHERE id=$1", copied[1].ID).Scan(&clientKey); err != nil || clientKey != "" {
		t.Fatalf("client=%q %v", clientKey, err)
	}
	if _, err = db.SaveMessage(f.TargetTopicID, req.ActorUserID, "too early", "text"); err == nil {
		t.Fatal("pending write accepted")
	}
	if writable, err := db.SessionForkWritable(context.Background(), f.TargetTopicID); err != nil || writable {
		t.Fatalf("pending writable=%v %v", writable, err)
	}
	if _, err = db.GetSessionFork(context.Background(), req.AgentUID, req.RequestID, ""); !errors.Is(err, store.ErrSessionForkNotFound) {
		t.Fatalf("owner isolation %v", err)
	}
	if _, err = db.GetBotSessionFork(context.Background(), req.ActorUserID, f.TargetTopicID); !errors.Is(err, store.ErrSessionForkNotFound) {
		t.Fatalf("bot isolation %v", err)
	}
	ready := forkComplete(t, db, f, "ready")
	if ready.State != "ready" {
		t.Fatalf("ready %#v", ready)
	}
	if _, err = db.SaveMessage(f.TargetTopicID, req.ActorUserID, "new independent turn", "text"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SaveMessage(req.SourceTopicID, req.ActorUserID, "source advanced", "text"); err != nil {
		t.Fatal(err)
	}
	if retry := forkComplete(t, db, f, "failed"); retry.State != "ready" {
		t.Fatalf("terminal ready regressed %#v", retry)
	}
	// Deleting the source cannot cascade into cloned messages or provenance.
	if _, err = db.db.Exec("DELETE FROM topics WHERE id=$1", req.SourceTopicID); err != nil {
		t.Fatal(err)
	}
	copied, err = db.GetMessages(f.TargetTopicID, 100, 0)
	if err != nil || len(copied) != 3 {
		t.Fatalf("target after source delete %d %v", len(copied), err)
	}
	if copied[0].ContentBlocks[0].Payload["url"] != "/uploads/files/retained.pdf" {
		t.Fatal("lost attachment URL")
	}
	if old := forkComplete(t, db, f, "ready"); old.State != "ready" {
		t.Fatal("ready invalidated by source deletion")
	}
	if len(original) != 2 {
		t.Fatal("invalid fixture")
	}
}

func TestSessionForkPostgresDetectsSourceMutations(t *testing.T) {
	for _, mutation := range []string{"insert", "edit", "delete", "delete-topic", "binding", "members"} {
		t.Run(mutation, func(t *testing.T) {
			db, req := newSessionForkPostgres(t)
			if mutation == "members" {
				group, err := db.CreateGroup("source", req.ActorUserID)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.UpdateGroupKind(group, "agent_task"); err != nil {
					t.Fatal(err)
				}
				if err = db.AddGroupMember(group, req.AgentUID, "member"); err != nil {
					t.Fatal(err)
				}
				req.SourceTopicID = fmt.Sprintf("grp_%d", group)
			}
			id, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "before", "text")
			if err != nil {
				t.Fatal(err)
			}
			f := forkCreate(t, db, req)
			switch mutation {
			case "insert":
				_, err = db.SaveMessage(req.SourceTopicID, req.ActorUserID, "new", "text")
			case "edit":
				_, err = db.db.Exec("UPDATE messages SET content='edited' WHERE id=$1", id)
			case "delete":
				_, err = db.db.Exec("DELETE FROM messages WHERE id=$1", id)
			case "delete-topic":
				_, err = db.db.Exec("DELETE FROM topics WHERE id=$1", req.SourceTopicID)
			case "binding":
				_, err = db.db.Exec("UPDATE bot_config SET body_id='another-body' WHERE user_id=$1", req.AgentUID)
			case "members":
				gid, _ := strconv.ParseInt(strings.TrimPrefix(req.SourceTopicID, "grp_"), 10, 64)
				err = db.RemoveGroupMember(gid, req.AgentUID)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := forkComplete(t, db, f, "ready")
			if got.State != "failed" || got.Error == "" {
				t.Fatalf("mutation accepted %#v", got)
			}
			if repeat := forkComplete(t, db, f, "ready"); repeat.State != "failed" {
				t.Fatalf("revived failed %#v", repeat)
			}
			if _, err = db.SaveMessage(f.TargetTopicID, req.ActorUserID, "blocked", "text"); err == nil {
				t.Fatal("failed target writable")
			}
		})
	}
}

func TestSessionForkPostgresWaitsForConcurrentSourceWrite(t *testing.T) {
	db, req := newSessionForkPostgres(t)
	id, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "before", "text")
	if err != nil {
		t.Fatal(err)
	}
	f := forkCreate(t, db, req)
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE messages SET content='concurrent' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := db.CompleteSessionFork(context.Background(), req.AgentUID, types.SessionForkCompletion{BodyID: req.BodyID, RequestID: f.RequestID, TargetTopicID: f.TargetTopicID, State: "ready"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("completion did not await concurrent writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Serializable PostgreSQL may abort the first attempt, which is safe to retry.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion stuck")
	}
	got := forkComplete(t, db, f, "ready")
	if got.State != "failed" || got.Error != "source_changed_retry_needed" {
		t.Fatalf("concurrent mutation not detected %#v", got)
	}
}

func TestSessionForkPostgresConcurrentRequestNoDuplicate(t *testing.T) {
	db, req := newSessionForkPostgres(t)
	if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "before", "text"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *types.SessionFork, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var f *types.SessionFork
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				f, err = db.CreateSessionFork(context.Background(), req)
				if err == nil {
					results <- f
					return
				}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var target string
	for f := range results {
		if target != "" && target != f.TargetTopicID {
			t.Fatal("duplicate target")
		}
		target = f.TargetTopicID
	}
	var count int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM session_forks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("fork rows=%d %v", count, err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM "groups"`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("group rows=%d %v", count, err)
	}
}

func TestSessionForkPostgresOldPendingAndWrongBody(t *testing.T) {
	db, req := newSessionForkPostgres(t)
	f := forkCreate(t, db, req)
	if _, err := db.db.Exec("UPDATE session_forks SET created_at=CURRENT_TIMESTAMP - INTERVAL '2 days'"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSessionFork(context.Background(), req.ActorUserID, req.RequestID, "")
	if err != nil || got.State != "pending" {
		t.Fatalf("GET mutated old pending %#v %v", got, err)
	}
	_, err = db.CompleteSessionFork(context.Background(), req.AgentUID, types.SessionForkCompletion{BodyID: "wrong", RequestID: f.RequestID, TargetTopicID: f.TargetTopicID, State: "ready"})
	if !errors.Is(err, store.ErrSessionForkForbidden) {
		t.Fatalf("wrong body %v", err)
	}
	if got := forkComplete(t, db, f, "ready"); got.State != "ready" {
		t.Fatal("valid delayed completion failed")
	}
}

func TestSessionForkPostgresPendingTargetMutationFailsCompletion(t *testing.T) {
	for _, mutation := range []string{"history", "members"} {
		t.Run(mutation, func(t *testing.T) {
			db, req := newSessionForkPostgres(t)
			if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "history", "text"); err != nil {
				t.Fatal(err)
			}
			f := forkCreate(t, db, req)
			if mutation == "history" {
				if _, err := db.db.Exec("DELETE FROM messages WHERE topic_id=$1", f.TargetTopicID); err != nil {
					t.Fatal(err)
				}
			} else {
				gid, _ := strconv.ParseInt(strings.TrimPrefix(f.TargetTopicID, "grp_"), 10, 64)
				if err := db.RemoveGroupMember(gid, req.AgentUID); err != nil {
					t.Fatal(err)
				}
			}
			got := forkComplete(t, db, f, "ready")
			if got.State != "failed" {
				t.Fatalf("changed target accepted %#v", got)
			}
		})
	}
}
