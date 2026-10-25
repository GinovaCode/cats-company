package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMessageSearchExecutionBudget(t *testing.T) {
	statement, err := mysqlMessageSearchQueryForContext(context.Background())
	if err != nil || statement != mysqlMessageSearchQuery {
		t.Fatalf("default query: %v", err)
	}
	if !strings.Contains(statement, fmt.Sprintf("SELECT /*+ MAX_EXECUTION_TIME(%d) */", mysqlMessageSearchMaxExecutionTime.Milliseconds())) {
		t.Fatal("default budget must be a top-level SELECT hint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := mysqlMessageSearchQueryForContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first, mysqlMessageSearchExecutionHint) {
		t.Fatal("short request retained the default budget")
	}
	// A later page must use the remaining deadline, not restart the original budget.
	later, cancelLater := context.WithDeadline(ctx, time.Now().Add(200*time.Millisecond))
	defer cancelLater()
	second, err := mysqlMessageSearchQueryForContext(later)
	if err != nil || second == first {
		t.Fatalf("page budget was not refreshed: %v", err)
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := mysqlMessageSearchQueryForContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
}

// Invoked from the connection-local fixture, after its other assertions, because
// driver cancellation may close that fixture's connection and drop its tables.
func testMySQLMessageSearchExecutionLimit(t *testing.T, adapter *Adapter, dsn string, fixtureQuery func(string) string) {
	t.Helper()
	for _, statement := range []string{
		`SET SESSION cte_max_recursion_depth = 30000`,
		`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks)
 WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<30000)
 SELECT 10000+n,'grp_1',2,'ordinary body','text',
 JSON_ARRAY(JSON_OBJECT('type','thinking','thinking',REPEAT('internal ',64)),
 JSON_OBJECT('type','assistant_text','text',REPEAT('visible answer ',100))) FROM seq`,
	} {
		if _, err := adapter.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	monitor, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	monitorCtx, cancelMonitor := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelMonitor()
	assertStopped := func(connectionID int64) {
		t.Helper()
		var command string
		err := monitor.QueryRowContext(monitorCtx, `SELECT COMMAND FROM information_schema.PROCESSLIST WHERE ID=?`, connectionID).Scan(&command)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if command != "Sleep" {
			t.Fatalf("original MySQL statement still executing: connection=%d command=%s", connectionID, command)
		}
	}
	var connectionID int64
	if err := adapter.db.QueryRow(`SELECT CONNECTION_ID()`).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	statement, err := mysqlMessageSearchQueryForContext(shortCtx)
	cancelShort()
	if err != nil {
		t.Fatal(err)
	}
	statement = fixtureQuery(statement)
	// Keep the client alive well beyond the hint to prove the server, not Go,
	// interrupts the actual production search statement with MySQL error 3024.
	start := time.Now()
	rows, queryErr := adapter.db.QueryContext(monitorCtx, statement, "all", "missing-expiry-probe", 1, 200, 200, 0)
	if queryErr == nil {
		_, _, queryErr = scanMySQLMessageSearch(rows, "missing-expiry-probe", "all", 20)
		rows.Close()
	}
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(queryErr, &mysqlErr) || mysqlErr.Number != 3024 {
		t.Fatalf("expected server execution timeout 3024, got %v", queryErr)
	}
	if monitorCtx.Err() != nil {
		t.Fatal("client deadline expired before the database timeout")
	}
	if !errors.Is(mysqlMessageSearchError(queryErr), context.DeadlineExceeded) {
		t.Fatal("server timeout was not classified as deadline exceeded")
	}
	assertStopped(connectionID)
	t.Logf("MySQL returned 3024 after %s; original connection is idle", time.Since(start))

	// Reproduce a short client deadline as well. The independently installed
	// server budget must stop the scan even if the driver discards its socket.
	if err := adapter.db.QueryRow(`SELECT CONNECTION_ID()`).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	clientCtx, cancelClient := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelClient()
	statement, err = mysqlMessageSearchQueryForContext(clientCtx)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	rows, queryErr = adapter.db.QueryContext(clientCtx, fixtureQuery(statement), "all", "missing-expiry-probe", 1, 200, 200, 0)
	if queryErr == nil {
		_, _, queryErr = scanMySQLMessageSearch(rows, "missing-expiry-probe", "all", 20)
		rows.Close()
	}
	if queryErr == nil {
		t.Fatal("slow fixture completed without exercising the timeout")
	}
	clientElapsed := time.Since(start)
	<-time.After(100 * time.Millisecond)
	assertStopped(connectionID)
	t.Logf("short-context search returned after %s; original statement is no longer executing 100ms later", clientElapsed)
}
