package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// The CI administrator configures the isolated service, never this test account.
// No CREATE DATABASE, global settings, mysql.user reads or destructive schema reset.
func openSessionForkMySQL(t *testing.T, wantTrust int) *Adapter {
	t.Helper()
	dsn := os.Getenv("CATS_SESSION_FORK_MYSQL_TEST_DSN")
	if dsn == "" {
		if os.Getenv("CATS_SESSION_FORK_MYSQL_EXPECT_PERMISSION_DENIED") == "1" {
			t.Fatal("permission-denied CI check requires CATS_SESSION_FORK_MYSQL_TEST_DSN")
		}
		t.Skip("set CATS_SESSION_FORK_MYSQL_TEST_DSN for real MySQL fork integration")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid MySQL test DSN")
	}
	if cfg.DBName == "" || !cfg.ParseTime {
		t.Fatal("MySQL fork test DSN must select an existing test database and parseTime=true")
	}
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 15 * time.Second
	db := &Adapter{}
	t.Cleanup(func() { db.Close() })
	if err := db.Open(cfg.FormatDSN()); err != nil {
		t.Fatal(err)
	}
	var account, roles, version string
	var binlog, trust int
	if err := db.db.QueryRow(`SELECT CURRENT_USER(), CURRENT_ROLE(), VERSION(), @@GLOBAL.log_bin, @@GLOBAL.log_bin_trust_function_creators`).Scan(&account, &roles, &version, &binlog, &trust); err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(strings.SplitN(account, "@", 2)[0], "root") || roles != "NONE" {
		t.Fatalf("fork tests require a non-root account with direct database grants, account=%s roles=%s", account, roles)
	}
	rows, err := db.db.Query("SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := sessionForkMySQLCheckGrants(cfg.DBName, grants); err != nil {
		t.Fatal(err)
	}
	if binlog != 1 || trust != wantTrust {
		t.Fatalf("require log_bin=1 and log_bin_trust_function_creators=%d; got %d/%d", wantTrust, binlog, trust)
	}
	t.Logf("MySQL %s, CURRENT_USER=%s, log_bin=%d, trust=%d, no global privileges or GRANT OPTION", version, account, binlog, trust)
	return db
}

func sessionForkMySQLCheckGrants(database string, grants []string) error {
	if len(grants) == 0 {
		return errors.New("SHOW GRANTS returned no account evidence")
	}
	databaseScope := "`" + strings.ReplaceAll(database, "`", "``") + "`.*"
	for _, grant := range grants {
		upper := strings.ToUpper(grant)
		parts := strings.SplitN(grant, " ON ", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "GRANT ") || strings.Contains(upper, "WITH GRANT OPTION") {
			return fmt.Errorf("fork test account must have direct grants without roles or GRANT OPTION: %s", grant)
		}
		scope := strings.SplitN(parts[1], " TO ", 2)
		if len(scope) != 2 || (scope[0] == "*.*" && parts[0] != "GRANT USAGE") || parts[0] == "GRANT PROXY" {
			return fmt.Errorf("fork test account must have no global privileges except USAGE: %s", grant)
		}
		if scope[0] != "*.*" && scope[0] != databaseScope {
			return fmt.Errorf("fork test account grants must be scoped only to %s: %s", databaseScope, grant)
		}
	}
	return nil
}

func TestSessionForkMySQLRestrictedSchema(t *testing.T) {
	if os.Getenv("CATS_SESSION_FORK_MYSQL_EXPECT_PERMISSION_DENIED") != "1" {
		t.Skip("negative CI stage only: set CATS_SESSION_FORK_MYSQL_EXPECT_PERMISSION_DENIED=1")
	}
	db := openSessionForkMySQL(t, 0)
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE() AND TRIGGER_NAME IN ('session_fork_message_insert','session_fork_message_update','session_fork_message_delete')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("restricted-schema stage must precede trigger installation: triggers=%d err=%v", count, err)
	}
	err := db.CreateSchema()
	var cause *driver.MySQLError
	if !errors.As(err, &cause) || cause.Number != 1419 {
		t.Fatalf("expected wrapped MySQL 1419 from ordinary-account CreateSchema, got %v", err)
	}
	for _, want := range []string{"administrator", "log_bin_trust_function_creators=1", "TRIGGER", "will not change global settings", "skip required triggers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in error: %v", want, err)
		}
	}
	// MySQL DDL partially commits; retain it so the positive CI stage tests recovery.
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='topics' AND COLUMN_NAME IN ('history_revision','fork_state')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected both columns to survive failed trigger installation: %d %v", count, err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM session_forks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected empty durable fork table after partial DDL: %d %v", count, err)
	}
}

func newSessionForkMySQL(t *testing.T) (*Adapter, types.SessionForkRequest) {
	t.Helper()
	if os.Getenv("CATS_SESSION_FORK_MYSQL_EXPECT_PERMISSION_DENIED") == "1" {
		t.Skip("positive fork tests run after the administrator enables trust")
	}
	db := openSessionForkMySQL(t, 1)
	for i := 0; i < 2; i++ {
		if err := db.CreateSchema(); err != nil {
			t.Fatalf("schema recovery/reinstallation %d: %v", i, err)
		}
	}
	var installed int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE() AND EVENT_OBJECT_TABLE='messages' AND ACTION_TIMING='BEFORE' AND DEFINER=CURRENT_USER() AND ((TRIGGER_NAME='session_fork_message_insert' AND EVENT_MANIPULATION='INSERT') OR (TRIGGER_NAME='session_fork_message_update' AND EVENT_MANIPULATION='UPDATE') OR (TRIGGER_NAME='session_fork_message_delete' AND EVENT_MANIPULATION='DELETE'))`).Scan(&installed); err != nil || installed != 3 {
		t.Fatalf("expected three message triggers defined by the ordinary account: count=%d err=%v", installed, err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	req := types.SessionForkRequest{RequestID: "request-1", BodyID: "body-test", Name: "Independent"}
	t.Cleanup(func() {
		if req.ActorUserID == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Only rows owned by this test's freshly allocated actor are removed.
		rows, err := db.db.QueryContext(ctx, "SELECT id FROM topics WHERE owner_id=?", req.ActorUserID)
		if err != nil {
			t.Errorf("fixture topic lookup: %v", err)
			return
		}
		var topics []string
		for rows.Next() {
			var topic string
			if err := rows.Scan(&topic); err != nil {
				t.Errorf("fixture topic scan: %v", err)
				rows.Close()
				return
			}
			topics = append(topics, topic)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Errorf("fixture topic lookup: %v", err)
			return
		}
		// Do not join topics in DELETE: the message trigger itself updates topics.
		for _, topic := range topics {
			if _, err := db.db.ExecContext(ctx, "DELETE FROM messages WHERE topic_id=?", topic); err != nil {
				t.Errorf("fixture message cleanup: %v", err)
			}
		}
		for _, q := range []string{
			"DELETE FROM topics WHERE owner_id=?",
			"DELETE FROM `groups` WHERE owner_id=?",
			"DELETE FROM session_forks WHERE actor_user_id=?",
		} {
			if _, err := db.db.ExecContext(ctx, q, req.ActorUserID); err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
		if _, err := db.db.ExecContext(ctx, "DELETE FROM users WHERE id IN (?,?)", req.ActorUserID, req.AgentUID); err != nil {
			t.Errorf("fixture account cleanup: %v", err)
		}
	})
	var err error
	req.ActorUserID, err = db.CreateUser(&types.User{Username: fmt.Sprintf("fork-owner-%x", suffix), AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	req.AgentUID, err = db.CreateUser(&types.User{Username: fmt.Sprintf("fork-agent-%x", suffix), AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO bot_config (user_id,owner_id,body_id) VALUES (?,?,?)`, req.AgentUID, req.ActorUserID, req.BodyID); err != nil {
		t.Fatal(err)
	}
	req.SourceTopicID = fmt.Sprintf("p2p_%d_%d", req.ActorUserID, req.AgentUID)
	if err := db.CreateTopic(req.SourceTopicID, "p2p", req.ActorUserID); err != nil {
		t.Fatal(err)
	}
	return db, req
}

func mysqlForkCreate(t *testing.T, db *Adapter, req types.SessionForkRequest) *types.SessionFork {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := db.CreateSessionFork(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mysqlForkComplete(t *testing.T, db *Adapter, f *types.SessionFork, state string) *types.SessionFork {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := db.CompleteSessionFork(ctx, f.AgentUID, types.SessionForkCompletion{BodyID: f.BodyID, RequestID: f.RequestID, TargetTopicID: f.TargetTopicID, State: state})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mysqlForkRevision(t *testing.T, db *Adapter, topic string) int64 {
	t.Helper()
	var revision int64
	if err := db.db.QueryRow("SELECT history_revision FROM topics WHERE id=?", topic).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestSessionForkMySQLCloneAndTerminalRetry(t *testing.T) {
	db, req := newSessionForkMySQL(t)
	blocks := []types.ContentBlock{{Type: "file", Payload: map[string]interface{}{"url": "/uploads/files/retained.pdf", "name": "report.pdf", "size": float64(12)}}}
	first, _, err := db.SaveMessageWithMetadata(req.SourceTopicID, req.ActorUserID, "visible", blocks, "normal", "user", "file", 0, "original-key", map[string]interface{}{"custom": "keep", "catsco_identity": map[string]interface{}{"token": "drop"}, "xiaoba_runtime": map[string]interface{}{"secret": "drop"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SaveMessageWithReply(req.SourceTopicID, req.AgentUID, "answer", "text", first)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err := db.db.Exec("UPDATE messages SET created_at=? WHERE topic_id=?", stamp, req.SourceTopicID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("UPDATE messages SET reply_to=? WHERE id=?", second, first); err != nil {
		t.Fatal(err)
	}
	original, err := db.GetMessages(req.SourceTopicID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := mysqlForkCreate(t, db, req)
	if f.State != "pending" || f.SourceLastSeq != second || len(f.MessageIDMap) != 2 || f.SourceRevision != mysqlForkRevision(t, db, req.SourceTopicID) {
		t.Fatalf("fork boundary/state: %#v", f)
	}
	duplicate := mysqlForkCreate(t, db, req)
	if duplicate.TargetTopicID != f.TargetTopicID || !reflect.DeepEqual(duplicate.MessageIDMap, f.MessageIDMap) {
		t.Fatal("duplicate request created a different target or mapping")
	}
	changed := req
	changed.Name = "Different"
	if _, err := db.CreateSessionFork(context.Background(), changed); !errors.Is(err, store.ErrSessionForkConflict) {
		t.Fatalf("request payload conflict: %v", err)
	}
	copied, err := db.GetMessages(f.TargetTopicID, 100, 0)
	if err != nil || len(copied) != 2 {
		t.Fatalf("copied history: count=%d err=%v", len(copied), err)
	}
	for i, m := range copied {
		o := original[i]
		if m.ID == o.ID || f.MessageIDMap[strconv.FormatInt(o.ID, 10)] != m.ID || m.FromUID != o.FromUID || !m.CreatedAt.Equal(o.CreatedAt) || !m.CreatedAt.Equal(stamp) || m.Content != o.Content || m.MsgType != o.MsgType || m.Mode != o.Mode || m.Role != o.Role || !reflect.DeepEqual(m.ContentBlocks, o.ContentBlocks) {
			t.Fatalf("message was not independently preserved: source=%#v target=%#v", o, m)
		}
		var clientKey sql.NullString
		if err := db.db.QueryRow("SELECT client_msg_id FROM messages WHERE id=?", m.ID).Scan(&clientKey); err != nil || clientKey.Valid {
			t.Fatalf("copied ingress key: %#v %v", clientKey, err)
		}
	}
	if copied[0].ReplyTo != copied[1].ID || copied[1].ReplyTo != copied[0].ID || copied[0].Metadata["custom"] != "keep" || copied[0].Metadata["catsco_identity"] != nil || copied[0].Metadata["xiaoba_runtime"] != nil {
		t.Fatalf("reply/metadata remapping: %#v %#v", copied[0], copied[1])
	}
	after, err := db.GetMessages(req.SourceTopicID, 100, 0)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Fatalf("clone modified source: %v", err)
	}
	if _, err := db.GetSessionFork(context.Background(), req.AgentUID, req.RequestID, ""); !errors.Is(err, store.ErrSessionForkNotFound) {
		t.Fatalf("user-scoped provenance: %v", err)
	}
	if _, err := db.GetBotSessionFork(context.Background(), req.ActorUserID, f.TargetTopicID); !errors.Is(err, store.ErrSessionForkNotFound) {
		t.Fatalf("bot-scoped provenance: %v", err)
	}
	if ready := mysqlForkComplete(t, db, f, "ready"); ready.State != "ready" {
		t.Fatalf("completion: %#v", ready)
	}
	if _, err := db.SaveMessage(f.TargetTopicID, req.ActorUserID, "independent progress", "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "source advanced", "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("DELETE FROM topics WHERE id=?", req.SourceTopicID); err != nil {
		t.Fatal(err)
	}
	f.BodyID = "old-body"
	if retry := mysqlForkComplete(t, db, f, "failed"); retry.State != "ready" {
		t.Fatalf("terminal ready regressed: %#v", retry)
	}
	copied, err = db.GetMessages(f.TargetTopicID, 100, 0)
	if err != nil || len(copied) != 3 {
		t.Fatalf("copy lost after source deletion: count=%d err=%v", len(copied), err)
	}
	if copied[0].ContentBlocks[0].Payload["url"] != "/uploads/files/retained.pdf" {
		t.Fatal("copied attachment reference lost")
	}
}

func TestSessionForkMySQLDatabaseFence(t *testing.T) {
	for _, state := range []string{"pending", "failed"} {
		t.Run(state, func(t *testing.T) {
			db, req := newSessionForkMySQL(t)
			if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "original", "text"); err != nil {
				t.Fatal(err)
			}
			f := mysqlForkCreate(t, db, req)
			if state == "failed" {
				if got := mysqlForkComplete(t, db, f, "failed"); got.State != "failed" {
					t.Fatalf("failed ack: %#v", got)
				}
			}
			before := mysqlForkRevision(t, db, f.TargetTopicID)
			for _, q := range []string{
				"INSERT INTO messages (topic_id,from_uid,content,msg_type) VALUES (?,?,'blocked','text')",
				"UPDATE messages SET content='blocked' WHERE topic_id=? AND from_uid=?",
			} {
				_, err := db.db.Exec(q, f.TargetTopicID, req.ActorUserID)
				var cause *driver.MySQLError
				if !errors.As(err, &cause) || cause.Number != 1644 || cause.Message != "session_fork_not_ready" {
					t.Fatalf("database fence did not reject write: %v", err)
				}
				if after := mysqlForkRevision(t, db, f.TargetTopicID); after != before {
					t.Fatalf("failed statement left revision changes: %d -> %d", before, after)
				}
			}
			messages, err := db.GetMessages(f.TargetTopicID, 100, 0)
			if err != nil || len(messages) != 1 || messages[0].Content != "original" {
				t.Fatalf("blocked write left message changes: %#v %v", messages, err)
			}
			if writable, err := db.SessionForkWritable(context.Background(), f.TargetTopicID); err != nil || writable {
				t.Fatalf("fence state: writable=%v err=%v", writable, err)
			}
			if state == "failed" {
				if retry := mysqlForkComplete(t, db, f, "ready"); retry.State != "failed" {
					t.Fatal("failed fork revived")
				}
			}
		})
	}
}

func TestSessionForkMySQLConcurrentSourceRevision(t *testing.T) {
	for _, mutation := range []string{"insert", "update", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			db, req := newSessionForkMySQL(t)
			id, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "before", "text")
			if err != nil {
				t.Fatal(err)
			}
			f := mysqlForkCreate(t, db, req)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := db.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mutation {
			case "insert":
				_, err = tx.ExecContext(ctx, "INSERT INTO messages (topic_id,from_uid,content,msg_type) VALUES (?,?,'concurrent','text')", req.SourceTopicID, req.ActorUserID)
			case "update":
				_, err = tx.ExecContext(ctx, "UPDATE messages SET content='concurrent' WHERE id=?", id)
			case "delete":
				_, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE id=?", id)
			}
			if err != nil {
				t.Fatal(err)
			}
			var revision int64
			if err := tx.QueryRowContext(ctx, "SELECT history_revision FROM topics WHERE id=?", req.SourceTopicID).Scan(&revision); err != nil || revision != f.SourceRevision+1 {
				t.Fatalf("mutation revision=%d want=%d err=%v", revision, f.SourceRevision+1, err)
			}
			type result struct {
				fork *types.SessionFork
				err  error
			}
			done := make(chan result, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				got, err := db.CompleteSessionFork(ctx, req.AgentUID, types.SessionForkCompletion{BodyID: req.BodyID, RequestID: f.RequestID, TargetTopicID: f.TargetTopicID, State: "ready"})
				done <- result{got, err}
			}()
			<-started
			select {
			case got := <-done:
				t.Fatalf("completion escaped the uncommitted source row lock: %#v %v", got.fork, got.err)
			case <-time.After(100 * time.Millisecond):
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.err != nil {
					var cause *driver.MySQLError
					if !errors.As(got.err, &cause) || (cause.Number != 1213 && cause.Number != 1205) {
						t.Fatalf("unexpected completion error: %v", got.err)
					}
				} else if got.fork.State != "failed" || got.fork.Error != "source_changed_retry_needed" {
					t.Fatalf("concurrent change accepted: %#v", got.fork)
				}
			case <-ctx.Done():
				t.Fatal("completion remained blocked after writer committed")
			}
			if got := mysqlForkComplete(t, db, f, "ready"); got.State != "failed" || got.Error != "source_changed_retry_needed" {
				t.Fatalf("revision mismatch revived: %#v", got)
			}
		})
	}
}

func TestSessionForkMySQLConcurrentRequestNoDuplicate(t *testing.T) {
	db, req := newSessionForkMySQL(t)
	if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "original", "text"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		fork *types.SessionFork
		err  error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			var f *types.SessionFork
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				f, err = db.CreateSessionFork(ctx, req)
				var cause *driver.MySQLError
				if err == nil || !errors.As(err, &cause) || (cause.Number != 1213 && cause.Number != 1205) {
					break
				}
			}
			results <- result{f, err}
		}()
	}
	close(start)
	var first *types.SessionFork
	for i := 0; i < 8; i++ {
		got := <-results
		if got.err != nil {
			t.Errorf("concurrent create: %v", got.err)
			continue
		}
		if first == nil {
			first = got.fork
		} else if got.fork.TargetTopicID != first.TargetTopicID || !reflect.DeepEqual(got.fork.MessageIDMap, first.MessageIDMap) {
			t.Error("same request produced different targets/mappings")
		}
	}
	for _, q := range []string{"SELECT COUNT(*) FROM session_forks WHERE actor_user_id=?", "SELECT COUNT(*) FROM `groups` WHERE owner_id=?"} {
		var count int
		if err := db.db.QueryRow(q, req.ActorUserID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("duplicate durable records: count=%d err=%v", count, err)
		}
	}
}

func TestSessionForkMySQLCloneRollback(t *testing.T) {
	db, req := newSessionForkMySQL(t)
	id, err := db.SaveMessageWithReply(req.SourceTopicID, req.ActorUserID, "invalid source reply", "text", int64(9223372036854775807))
	if err != nil {
		t.Fatal(err)
	}
	before := mysqlForkRevision(t, db, req.SourceTopicID)
	// The unmappable reply fails the second pass, after target rows were inserted.
	if f, err := db.CreateSessionFork(context.Background(), req); f != nil || !errors.Is(err, store.ErrSessionForkSource) {
		t.Fatalf("expected clone rollback on missing reply mapping: %#v %v", f, err)
	}
	for _, check := range []struct {
		query string
		count int
	}{
		{"SELECT COUNT(*) FROM session_forks WHERE actor_user_id=?", 0},
		{"SELECT COUNT(*) FROM `groups` WHERE owner_id=?", 0},
		{"SELECT COUNT(*) FROM topics WHERE owner_id=?", 1},
		{"SELECT COUNT(*) FROM messages WHERE from_uid=?", 1},
	} {
		var count int
		if err := db.db.QueryRow(check.query, req.ActorUserID).Scan(&count); err != nil || count != check.count {
			t.Fatalf("rollback residue: count=%d want=%d err=%v", count, check.count, err)
		}
	}
	if mysqlForkRevision(t, db, req.SourceTopicID) != before {
		t.Fatal("failed clone changed source revision")
	}
	if _, err := db.db.Exec("UPDATE messages SET reply_to=NULL WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if f := mysqlForkCreate(t, db, req); f.State != "pending" || len(f.MessageIDMap) != 1 {
		t.Fatalf("same request could not retry after rollback: %#v", f)
	}
}

func TestSessionForkMySQLTargetDeleteInvalidatesCompletion(t *testing.T) {
	db, req := newSessionForkMySQL(t)
	if _, err := db.SaveMessage(req.SourceTopicID, req.ActorUserID, "original", "text"); err != nil {
		t.Fatal(err)
	}
	f := mysqlForkCreate(t, db, req)
	if _, err := db.db.Exec("DELETE FROM messages WHERE topic_id=?", f.TargetTopicID); err != nil {
		t.Fatal(err)
	}
	if revision := mysqlForkRevision(t, db, f.TargetTopicID); revision != f.TargetRevision+1 {
		t.Fatalf("target deletion revision=%d want=%d", revision, f.TargetRevision+1)
	}
	if got := mysqlForkComplete(t, db, f, "ready"); got.State != "failed" || got.Error != "target_changed_retry_needed" {
		t.Fatalf("changed target accepted: %#v", got)
	}
}
