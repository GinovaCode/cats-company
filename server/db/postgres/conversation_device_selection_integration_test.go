package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Opt-in only: point this variable at a disposable local test database.
func TestDeviceSelectionDatabaseContract(t *testing.T) {
	dsn := os.Getenv("CATS_DEVICE_SELECTION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set isolated device-selection database DSN for real-engine acceptance")
	}
	first := &Adapter{}
	if err := first.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for _, ddl := range []string{createUsersTable, createConversationDeviceSelectionsTable, createConversationDeviceSelectionsTable} {
		if _, err := first.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	username := fmt.Sprintf("device-choice-%d", time.Now().UnixNano())
	var actor int64
	if err := first.db.QueryRow("INSERT INTO users (username, pass_hash) VALUES ($1, $2) RETURNING id", username, []byte("test-only")).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer first.db.Exec("DELETE FROM users WHERE id = $1", actor)
	ctx := context.Background()
	session := "session:v2:catscompany:group:grp_1:actor:User-A:agent:usr42"
	next := store.ConversationDeviceSelection{DeviceID: "device-a", Revision: "revision-a", Source: "user_confirmation", UpdatedAt: time.Now().UnixMilli()}
	if won, err := first.CompareConversationDeviceSelection(ctx, actor, session, "", next); err != nil || !won {
		t.Fatal("initial insert", won, err)
	}
	if won, err := first.CompareConversationDeviceSelection(ctx, actor, session, "", next); err != nil || won {
		t.Fatal("duplicate insert", won, err)
	}
	// Independent connection pool models a different server process, not a cache.
	second := &Adapter{}
	if err := second.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	read, err := second.GetConversationDeviceSelection(ctx, actor, session)
	if err != nil || read == nil || read.DeviceID != "device-a" {
		t.Fatal("reopen", read, err)
	}
	// Session and revision comparisons must be case-sensitive on MySQL too.
	if read, err := second.GetConversationDeviceSelection(ctx, actor, "session:v2:catscompany:group:grp_1:actor:user-a:agent:usr42"); err != nil || read != nil {
		t.Fatal("case folded scope", read, err)
	}
	if read, err := second.GetConversationDeviceSelection(ctx, actor+1, session); err != nil || read != nil {
		t.Fatal("cross-account", read, err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := next
			candidate.DeviceID = fmt.Sprintf("device-%d", i)
			candidate.Revision = fmt.Sprintf("revision-%d", i)
			adapter := first
			if i%2 == 0 {
				adapter = second
			}
			won, err := adapter.CompareConversationDeviceSelection(ctx, actor, session, "revision-a", candidate)
			if err != nil {
				t.Error(err)
			}
			if won {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d", winners.Load())
	}
	// A stale page (including a differently-cased revision) cannot undo the winner.
	next.Revision = "stale-revision"
	if won, err := second.CompareConversationDeviceSelection(ctx, actor, session, "REVISION-A", next); err != nil || won {
		t.Fatal("stale/casefolded revision won", won, err)
	}
	if _, err := first.db.Exec("DELETE FROM users WHERE id = $1", actor); err != nil {
		t.Fatal(err)
	}
	if read, err := second.GetConversationDeviceSelection(ctx, actor, session); err != nil || read != nil {
		t.Fatal("user deletion did not cascade", read, err)
	}
}

// Execute the actual numbered migration, including rollback, inside a disposable
// transaction-local schema. Existing application tables must never be dropped.
func TestDeviceSelectionNumberedMigrationRealDatabase(t *testing.T) {
	dsn := os.Getenv("CATS_DEVICE_SELECTION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set isolated device-selection database DSN for real-engine acceptance")
	}
	adapter := &Adapter{}
	if err := adapter.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	tx, err := adapter.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema := fmt.Sprintf("device_migration_%d", time.Now().UnixNano())
	for _, statement := range []string{"CREATE SCHEMA " + schema, "SET LOCAL search_path TO " + schema, createUsersTable} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	up, err := os.ReadFile("../migrations/postgres/000049_conversation_device_selections.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../migrations/postgres/000049_conversation_device_selections.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(string(up)); err != nil {
		t.Fatal("up migration", err)
	}
	var actor int64
	if err := tx.QueryRow("INSERT INTO users (username, pass_hash) VALUES ($1, $2) RETURNING id", "migration-only", []byte("test-only")).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO conversation_device_selections (actor_uid, session_key, device_id, revision, source, updated_at_ms) VALUES ($1, 'session-test', 'device-a', 'revision-a', 'test', 1)", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(string(up)); err != nil {
		t.Fatal("repeated up migration", err)
	}
	var device string
	if err := tx.QueryRow("SELECT device_id FROM conversation_device_selections WHERE actor_uid = $1", actor).Scan(&device); err != nil || device != "device-a" {
		t.Fatal("migration erased choice", device, err)
	}
	if _, err := tx.Exec(string(down)); err != nil {
		t.Fatal("down migration", err)
	}
	var absent bool
	if err := tx.QueryRow("SELECT to_regclass($1) IS NULL", schema+".conversation_device_selections").Scan(&absent); err != nil || !absent {
		t.Fatal("down did not drop test table", err)
	}
	if _, err := tx.Exec(string(up)); err != nil {
		t.Fatal("up after rollback", err)
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM conversation_device_selections").Scan(&count); err != nil || count != 0 {
		t.Fatal("down/up should not resurrect choices", count, err)
	}
}
