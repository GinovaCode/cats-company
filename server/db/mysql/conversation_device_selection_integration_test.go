package mysql

import (
	"context"
	"fmt"
	"github.com/openchat/openchat/server/store"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in only: point this variable at a disposable local test database.
func TestDeviceSelectionDatabaseContract(t *testing.T) {
	dsn := os.Getenv("CATS_DEVICE_SELECTION_TEST_MYSQL_DSN")
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
	insert, err := first.db.Exec("INSERT INTO users (username, pass_hash) VALUES (?, ?)", username, []byte("test-only"))
	if err != nil {
		t.Fatal(err)
	}
	actor, err = insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer first.db.Exec("DELETE FROM users WHERE id = ?", actor)
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
	if _, err := first.db.Exec("DELETE FROM users WHERE id = ?", actor); err != nil {
		t.Fatal(err)
	}
	if read, err := second.GetConversationDeviceSelection(ctx, actor, session); err != nil || read != nil {
		t.Fatal("user deletion did not cascade", read, err)
	}
}
