package postgres

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// 应用目录订阅清单（设计 §3.3-9；迁移 000034）。
// 无行 = 默认可见；hidden=true 行 = 账号将其从清单中移除（屏蔽）。
// "添加"（恢复）= 删行。

func (a *Adapter) ListHiddenServiceAgentIDs(ctx context.Context, uid int64) ([]int64, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT agent_id FROM service_agent_subscriptions
		WHERE uid = $1 AND hidden`, uid)
	if err != nil {
		return nil, fmt.Errorf("list hidden service agents: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var agentID int64
		if err := rows.Scan(&agentID); err != nil {
			return nil, fmt.Errorf("scan hidden service agent: %w", err)
		}
		out = append(out, agentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden service agents: %w", err)
	}
	return out, nil
}

func (a *Adapter) SetServiceAgentSubscriptionHidden(ctx context.Context, uid, agentID int64, hidden bool) error {
	if !hidden {
		// 恢复默认可见 = 删行（幂等）。
		if _, err := a.db.ExecContext(ctx,
			`DELETE FROM service_agent_subscriptions WHERE uid = $1 AND agent_id = $2`, uid, agentID); err != nil {
			return fmt.Errorf("restore service agent subscription: %w", err)
		}
		return nil
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_agent_subscriptions (uid, agent_id, hidden, updated_at)
		VALUES ($1, $2, TRUE, clock_timestamp())
		ON CONFLICT (uid, agent_id) DO UPDATE SET hidden = TRUE, updated_at = clock_timestamp()`,
		uid, agentID); err != nil {
		return fmt.Errorf("hide service agent subscription: %w", err)
	}
	return nil
}

var _ store.ServiceAgentStore = (*Adapter)(nil)
