package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 应用目录订阅清单（000034）：schema 常量 / 编号迁移 / 屏蔽-恢复往返 / uid 隔离。

func TestServiceAgentSubscriptionsSchemaConstants(t *testing.T) {
	if !strings.Contains(createServiceAgentSubscriptionsTable, "PRIMARY KEY (uid, agent_id)") {
		t.Fatal("service_agent_subscriptions missing (uid, agent_id) primary key")
	}
	if !strings.Contains(createServiceAgentSubscriptionsTable, "hidden BOOLEAN") {
		t.Fatal("service_agent_subscriptions missing hidden column")
	}
	if !strings.Contains(createServiceAgentSubscriptionsTable, "REFERENCES service_agents(id) ON DELETE CASCADE") {
		t.Fatal("service_agent_subscriptions must reference service_agents with cascade")
	}
}

func TestServiceAgentSubscriptionsMigrationMatchesRuntimeSchema(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000034_service_agent_subscriptions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS service_agent_subscriptions", "PRIMARY KEY (uid, agent_id)"} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration missing %q: %s", want, up)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000034_service_agent_subscriptions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS service_agent_subscriptions") {
		t.Fatalf("down migration must be reversible: %s", down)
	}
}

// 000034 在 000027 之上幂等执行，且可回滚再上。
func TestServiceAgentSubscriptionsMigrationFilesExecute(t *testing.T) {
	db := pgTestDB(t)
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000034_service_agent_subscriptions.up.sql")
	execRetireV4MigrationFile(t, db, "000034_service_agent_subscriptions.up.sql") // 幂等重放
	execRetireV4MigrationFile(t, db, "000034_service_agent_subscriptions.down.sql")
	execRetireV4MigrationFile(t, db, "000034_service_agent_subscriptions.up.sql")
}

// 屏蔽 → 列出；重复屏蔽幂等；恢复（添加）→ 清空；uid 隔离。
func TestServiceAgentSubscriptionsHideRestore(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	// 默认可见（无行）
	hidden, err := db.ListHiddenServiceAgentIDs(ctx, 441)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("default hidden = %v err=%v, want empty", hidden, err)
	}

	// 屏蔽（幂等两次）
	for i := 0; i < 2; i++ {
		if err := db.SetServiceAgentSubscriptionHidden(ctx, 441, aID, true); err != nil {
			t.Fatalf("hide #%d: %v", i+1, err)
		}
	}
	hidden, err = db.ListHiddenServiceAgentIDs(ctx, 441)
	if err != nil || len(hidden) != 1 || hidden[0] != aID {
		t.Fatalf("after hide = %v err=%v, want [%d]", hidden, err, aID)
	}
	// 单行（UPSERT 不重复）
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_agent_subscriptions WHERE uid = 441`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows after double hide = %d err=%v, want 1", count, err)
	}

	// uid 隔离：442 不受影响
	if other, err := db.ListHiddenServiceAgentIDs(ctx, 442); err != nil || len(other) != 0 {
		t.Fatalf("uid 442 hidden = %v err=%v, want empty", other, err)
	}

	// 恢复（幂等两次）→ 行删除
	for i := 0; i < 2; i++ {
		if err := db.SetServiceAgentSubscriptionHidden(ctx, 441, aID, false); err != nil {
			t.Fatalf("restore #%d: %v", i+1, err)
		}
	}
	if hidden, err := db.ListHiddenServiceAgentIDs(ctx, 441); err != nil || len(hidden) != 0 {
		t.Fatalf("after restore = %v err=%v, want empty", hidden, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_agent_subscriptions WHERE uid = 441`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows after restore = %d err=%v, want 0", count, err)
	}
}
