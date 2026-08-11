-- ServerCLI: 0007 — 迁移与运维：service ownership 上报
-- 目的：节点 Agent 在心跳中上报本地 ServerCLI ownership（/etc/servercli/private/ownership.json），
-- 控制面集中记录每个节点的服务归属，供「迁移与运维」控制台展示 owner 状态机。
-- 兼容 SQLite 与 PostgreSQL 的纯 SQL。
CREATE TABLE IF NOT EXISTS service_ownership (
  node_id       TEXT NOT NULL,
  service       TEXT NOT NULL,
  owner         TEXT NOT NULL DEFAULT 'legacy-init',
  config_digest TEXT NOT NULL DEFAULT '',
  environment   TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (node_id, service)
);
CREATE INDEX IF NOT EXISTS idx_service_ownership_node ON service_ownership(node_id);
