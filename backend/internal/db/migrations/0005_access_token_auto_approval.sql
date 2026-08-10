-- ServerCLI: Access Token 自动审批体系（任务 019fe976）。
-- 将 AI Lease 的“匿名申请 + 人工审批/设备免审批规则”替换为
-- Access Token 鉴权 + 自动审批。兼容 SQLite 与 PostgreSQL：
-- 布尔为 INTEGER 0/1，时间为 TEXT RFC3339 UTC，由数据访问层归一化。

-- 访问令牌（仅存哈希与前缀；明文只在创建时返回一次）。
CREATE TABLE api_access_token (
    id                 TEXT PRIMARY KEY,
    environment_id     TEXT NOT NULL,
    name               TEXT NOT NULL,
    token_hash         TEXT NOT NULL UNIQUE,
    token_prefix       TEXT NOT NULL,
    created_by         TEXT,
    created_at         TEXT NOT NULL,
    expires_at         TEXT,          -- NULL 表示永久
    revoked_at         TEXT,
    revoked_by         TEXT,
    last_used_at       TEXT,
    last_used_ip       TEXT,
    usage_count        INTEGER NOT NULL DEFAULT 0,
    permission_version INTEGER NOT NULL DEFAULT 1,
    permissions_json   TEXT
);
CREATE INDEX idx_api_access_token_env ON api_access_token(environment_id);
CREATE INDEX idx_api_access_token_revoked ON api_access_token(revoked_at);

-- 令牌使用日志：每次可识别令牌的请求都记录一条。
CREATE TABLE api_token_usage_log (
    id               TEXT PRIMARY KEY,
    token_id         TEXT NOT NULL,
    environment_id   TEXT NOT NULL,
    request_id       TEXT,
    occurred_at      TEXT NOT NULL,
    method           TEXT NOT NULL,
    route            TEXT NOT NULL,   -- 规范化路由模板，不保存敏感查询值
    resource         TEXT,
    action           TEXT,
    source_ip        TEXT,
    user_agent       TEXT,
    status_code      INTEGER NOT NULL DEFAULT 0,
    outcome          TEXT NOT NULL,   -- success / denied / failure
    lease_request_id TEXT,
    lease_id         TEXT,
    token_state      TEXT NOT NULL    -- valid / expired / revoked
);
CREATE INDEX idx_token_usage_token ON api_token_usage_log(token_id, occurred_at);
CREATE INDEX idx_token_usage_lease ON api_token_usage_log(lease_id);

-- Lease 申请与 Lease 的令牌归属（历史数据允许为空，新数据必须绑定）。
ALTER TABLE ai_lease_request ADD COLUMN access_token_id TEXT;
CREATE INDEX idx_lease_request_token ON ai_lease_request(access_token_id);

ALTER TABLE ai_lease ADD COLUMN access_token_id TEXT;
CREATE INDEX idx_ai_lease_token ON ai_lease(access_token_id);
