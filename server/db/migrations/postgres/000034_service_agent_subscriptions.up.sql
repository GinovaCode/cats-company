-- 应用目录订阅清单（设计 §3.3-9 目录与订阅；2026-10-14 定"模式二：默认开放 + 个人清单"）。
-- 语义：清单跟账号（uid）；tools/list = 全部 active agent − 本表屏蔽集。
-- 无行 = 默认可见（底层开放）；hidden=true = 用户从自己的清单中移除（屏蔽，调用亦拒绝）；
-- "添加" = 删除本行（恢复默认可见）。行的双态设计为将来"白名单式"收紧留口。

CREATE TABLE IF NOT EXISTS service_agent_subscriptions (
    uid BIGINT NOT NULL,
    agent_id BIGINT NOT NULL REFERENCES service_agents(id) ON DELETE CASCADE,
    hidden BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (uid, agent_id)
);
