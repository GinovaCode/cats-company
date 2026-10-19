package mysql

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

var sessionForkColumns = []string{"actor_user_id", "request_id", "source_topic_id", "target_topic_id", "agent_uid", "body_id", "request_name", "source_revision", "target_revision", "source_last_seq", "message_id_map", "state", "error", "created_at"}

func TestSessionForkMySQLCloneTransaction(t *testing.T) {
	for _, failCopy := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "rollback"}[failCopy], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			ctx := context.Background()
			stamp := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT account_type FROM users").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_type"}).AddRow("human"))
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "one").WillReturnRows(sqlmock.NewRows(sessionForkColumns))
			mock.ExpectQuery("SELECT history_revision, fork_state FROM topics").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"history_revision", "fork_state"}).AddRow(2, ""))
			mock.ExpectQuery("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "body_id", "account_type", "state"}).AddRow(7, "body", "bot", 0))
			mock.ExpectQuery("SELECT id,from_uid,content,msg_type,content_blocks,mode,role,metadata,reply_to,created_at FROM messages").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"id", "from_uid", "content", "msg_type", "content_blocks", "mode", "role", "metadata", "reply_to", "created_at"}).AddRow(10, 7, "file", "file", `[{"type":"file","payload":{"url":"/uploads/files/report.pdf"}}]`, "normal", "user", `{"custom":"keep"}`, nil, stamp).AddRow(11, 9, "answer", "text", nil, "normal", "assistant", nil, 10, stamp))
			mock.ExpectExec("INSERT INTO `groups`").WithArgs("\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(20, 1))
			mock.ExpectExec("INSERT INTO group_members").WithArgs(int64(20), int64(7), int64(20), int64(9)).WillReturnResult(sqlmock.NewResult(0, 2))
			mock.ExpectExec("INSERT INTO topics").WithArgs("grp_20", "\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			copyExpectation := mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(7), "file", "file", `[{"type":"file","payload":{"url":"/uploads/files/report.pdf"}}]`, "normal", "user", `{"custom":"keep"}`, stamp)
			if failCopy {
				copyExpectation.WillReturnError(errors.New("copy failure"))
				mock.ExpectRollback()
			} else {
				copyExpectation.WillReturnResult(sqlmock.NewResult(100, 1))
				mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(9), "answer", "text", nil, "normal", "assistant", nil, stamp).WillReturnResult(sqlmock.NewResult(101, 1))
				mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(nil, `{"custom":"keep"}`, int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(int64(100), nil, int64(101)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT history_revision FROM topics").WithArgs("grp_20").WillReturnRows(sqlmock.NewRows([]string{"history_revision"}).AddRow(4))
				mock.ExpectExec("INSERT INTO session_forks").WithArgs(int64(7), "one", "p2p_7_9", "grp_20", int64(9), "body", "", int64(2), int64(4), int64(11), `{"10":100,"11":101}`, "pending", "", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE topics SET fork_state = 'pending'").WithArgs("grp_20").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			got, err := a.CreateSessionFork(ctx, types.SessionForkRequest{RequestID: "one", SourceTopicID: "p2p_7_9", ActorUserID: 7, AgentUID: 9, BodyID: "body"})
			if failCopy {
				if err == nil || got != nil {
					t.Fatal("copy failure committed")
				}
			} else if err != nil || got.State != "pending" || got.TargetTopicID != "grp_20" || got.MessageIDMap["11"] != 101 {
				t.Fatalf("clone %#v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLTerminalCompletionPrecedesSourceCheck(t *testing.T) {
	for _, state := range []string{"ready", "failed"} {
		t.Run(state, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE agent_uid").WithArgs(int64(9), "grp_20", "one").WillReturnRows(sqlmock.NewRows(sessionForkColumns).AddRow(7, "one", "p2p_7_9", "grp_20", 9, "body", "", 2, 4, 11, `{"10":100,"11":101}`, state, "", time.Now()))
			mock.ExpectCommit()
			got, err := (&Adapter{db: db}).CompleteSessionFork(context.Background(), 9, types.SessionForkCompletion{RequestID: "one", TargetTopicID: "grp_20", State: "ready", BodyID: "changed"})
			if err != nil || got.State != state {
				t.Fatalf("terminal %#v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLMigrationMatchesStartup(t *testing.T) {
	raw, err := os.ReadFile("../migrations/mysql/000041_session_forks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range sessionForkSchema {
		if !strings.Contains(string(raw), statement) {
			t.Fatalf("startup statement absent from migration: %s", statement)
		}
	}
}
