package mysql

import (
	"context"
	"testing"
)

// 应用目录订阅清单（PG 000034 同语义镜像）：屏蔽-恢复往返 + uid 隔离。
func TestMySQLServiceAgentSubscriptions(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)

	// 默认可见（无行）
	if hidden, err := db.ListHiddenServiceAgentIDs(ctx, 441); err != nil || len(hidden) != 0 {
		t.Fatalf("default hidden = %v err=%v, want empty", hidden, err)
	}

	// 屏蔽（幂等两次）
	for i := 0; i < 2; i++ {
		if err := db.SetServiceAgentSubscriptionHidden(ctx, 441, aID, true); err != nil {
			t.Fatalf("hide #%d: %v", i+1, err)
		}
	}
	hidden, err := db.ListHiddenServiceAgentIDs(ctx, 441)
	if err != nil || len(hidden) != 1 || hidden[0] != aID {
		t.Fatalf("after hide = %v err=%v, want [%d]", hidden, err, aID)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_agent_subscriptions WHERE uid = 441`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows after double hide = %d err=%v, want 1", count, err)
	}

	// uid 隔离
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
