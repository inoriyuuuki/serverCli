-- ServerCLI: 0007 — 部署管理（Deployment Management）
-- 目的：新增部署管理模块所需 12 张表：feature/release、OSS profile、
-- config profile、secret reference、target、target_secret、operation、
-- operation_target、step、backup、bootstrap_session。
-- 安全性：
--   * deployment_secret_reference 只保存引用（object_key/content_hash/
--     encryption_mode/size），绝不保存密钥正文（无 content 列）。
--   * oss_profile.access_key_id_enc / access_key_secret_enc 仅保存 service
--     层加密后的密文或密钥引用，绝不保存明文 AccessKey。
--   * encryption_mode 默认 'none'（V1），CHECK 限定合法取值。
-- 并发约束：
--   * uq_deployment_op_active_feature 部分唯一索引保证同一 feature 同时
--     最多存在一个 active（queued/running/awaiting_confirmation）操作。
--   * deployment_operation_target UNIQUE(operation_id, target_id) 防止同一
--     操作内重复目标。
-- 删除策略（注释）：
--   * deployment_target.feature_id -> ON DELETE RESTRICT：防止误删仍被目标
--     引用的 feature；其余 deployment_* 子表对 feature 的引用级联删除。
--   * release 被 target / operation / operation_target 引用时 ON DELETE
--     SET NULL（可选引用，保留历史行）。
--   * node 删除 -> 级联删除其 target / operation_target / step / backup /
--     bootstrap_session。
-- 兼容 SQLite 与 PostgreSQL：TEXT/INTEGER/BIGINT 时间戳用 TEXT ISO8601，
-- 无 ENUM（用 TEXT + CHECK 约束）；partial unique index 两者语法一致。

CREATE TABLE deployment_feature (
    id                     TEXT PRIMARY KEY,
    feature_key            TEXT NOT NULL UNIQUE,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    os                     TEXT NOT NULL,
    arch                   TEXT NOT NULL,
    config_schema_json     TEXT,
    backup_mode            TEXT NOT NULL DEFAULT 'none',
    rollback_capability    TEXT NOT NULL DEFAULT 'none',
    dependencies_json      TEXT,
    minimum_agent_version  TEXT NOT NULL DEFAULT '',
    default_version        TEXT,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

CREATE TABLE deployment_release (
    id                          TEXT PRIMARY KEY,
    feature_id                  TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE CASCADE,
    version                     TEXT NOT NULL,
    source_commit               TEXT,
    object_key                  TEXT NOT NULL,
    size                        BIGINT NOT NULL DEFAULT 0,
    sha256                      TEXT NOT NULL,
    signature                   TEXT,
    install_hook                TEXT NOT NULL DEFAULT '',
    update_hook                 TEXT NOT NULL DEFAULT '',
    backup_hook                 TEXT NOT NULL DEFAULT '',
    health_hook                 TEXT NOT NULL DEFAULT '',
    rollback_hook               TEXT NOT NULL DEFAULT '',
    backup_mode                 TEXT NOT NULL DEFAULT 'none',
    data_migration_metadata_json TEXT,
    manifest_hash               TEXT,
    created_at                  TEXT NOT NULL,
    UNIQUE(feature_id, version)
);
CREATE INDEX idx_deployment_release_feature ON deployment_release(feature_id);

CREATE TABLE oss_profile (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL UNIQUE,
    endpoint             TEXT NOT NULL,
    region               TEXT NOT NULL DEFAULT '',
    bucket               TEXT NOT NULL,
    prefix               TEXT NOT NULL DEFAULT '',
    access_key_id_enc    TEXT NOT NULL,
    access_key_secret_enc TEXT NOT NULL,
    is_private           INTEGER NOT NULL DEFAULT 1,
    last_tested_at       TEXT,
    last_test_result     TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE deployment_config_profile (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    scope_type    TEXT NOT NULL DEFAULT 'shared',
    scope_id      TEXT NOT NULL DEFAULT '',
    feature_id    TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE CASCADE,
    content_json  TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE(feature_id, scope_type, scope_id, name)
);
CREATE INDEX idx_deployment_config_profile_feature ON deployment_config_profile(feature_id);

CREATE TABLE deployment_secret_reference (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    feature_id      TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE CASCADE,
    scope_type      TEXT NOT NULL DEFAULT 'shared',
    scope_id        TEXT NOT NULL DEFAULT '',
    object_key      TEXT NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    content_hash    TEXT NOT NULL,
    encryption_mode TEXT NOT NULL DEFAULT 'none',
    size            BIGINT NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL,
    UNIQUE(feature_id, scope_type, scope_id, name),
    CHECK (encryption_mode IN ('none', 'aes-gcm', 'kms-envelope'))
);
CREATE INDEX idx_deployment_secret_ref_feature ON deployment_secret_reference(feature_id);

CREATE TABLE deployment_target (
    id                      TEXT PRIMARY KEY,
    feature_id              TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE RESTRICT,
    node_id                 TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    config_profile_id       TEXT REFERENCES deployment_config_profile(id) ON DELETE SET NULL,
    override_reference_json TEXT,
    desired_release_id      TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    current_release_id      TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    last_healthy_release_id TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    actual_status           TEXT NOT NULL DEFAULT 'pending',
    last_health_check_at    TEXT,
    config_revision         INTEGER NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    UNIQUE(feature_id, node_id)
);
CREATE INDEX idx_deployment_target_node ON deployment_target(node_id);
CREATE INDEX idx_deployment_target_status ON deployment_target(actual_status);

CREATE TABLE deployment_target_secret (
    id                  TEXT PRIMARY KEY,
    target_id           TEXT NOT NULL REFERENCES deployment_target(id) ON DELETE CASCADE,
    secret_reference_id TEXT NOT NULL REFERENCES deployment_secret_reference(id) ON DELETE CASCADE,
    binding_path        TEXT NOT NULL,
    version             INTEGER NOT NULL DEFAULT 1,
    content_hash        TEXT NOT NULL,
    encryption_mode     TEXT NOT NULL DEFAULT 'none',
    updated_at          TEXT NOT NULL,
    UNIQUE(target_id, secret_reference_id),
    CHECK (encryption_mode IN ('none', 'aes-gcm', 'kms-envelope'))
);
CREATE INDEX idx_deployment_target_secret_ref ON deployment_target_secret(secret_reference_id);

CREATE TABLE deployment_operation (
    id                 TEXT PRIMARY KEY,
    action             TEXT NOT NULL,
    feature_id         TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE CASCADE,
    release_id         TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    strategy           TEXT NOT NULL DEFAULT 'serial',
    status             TEXT NOT NULL DEFAULT 'draft',
    requested_by       TEXT NOT NULL DEFAULT '',
    reason             TEXT,
    environment_id     TEXT NOT NULL DEFAULT '',
    frozen_config_hash TEXT,
    created_at         TEXT NOT NULL,
    started_at         TEXT,
    finished_at        TEXT
);
CREATE INDEX idx_deployment_operation_feature ON deployment_operation(feature_id);
CREATE INDEX idx_deployment_operation_status ON deployment_operation(status);
-- 并发约束：同一 feature 同时最多一个 active 操作（SQLite/PostgreSQL 均支持 partial index）。
CREATE UNIQUE INDEX uq_deployment_op_active_feature
    ON deployment_operation(feature_id)
    WHERE status IN ('queued', 'running', 'awaiting_confirmation');

CREATE TABLE deployment_operation_target (
    id                 TEXT PRIMARY KEY,
    operation_id       TEXT NOT NULL REFERENCES deployment_operation(id) ON DELETE CASCADE,
    target_id          TEXT NOT NULL REFERENCES deployment_target(id) ON DELETE CASCADE,
    node_id            TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    status             TEXT NOT NULL DEFAULT 'pending',
    current_release_id TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    desired_release_id TEXT REFERENCES deployment_release(id) ON DELETE SET NULL,
    frozen_config_hash TEXT,
    frozen_secret_hash TEXT,
    error_message      TEXT,
    started_at         TEXT,
    finished_at        TEXT,
    UNIQUE(operation_id, target_id)
);
CREATE INDEX idx_deployment_op_target_target ON deployment_operation_target(target_id);
CREATE INDEX idx_deployment_op_target_node ON deployment_operation_target(node_id);

CREATE TABLE deployment_step (
    id                   TEXT PRIMARY KEY,
    operation_id         TEXT NOT NULL REFERENCES deployment_operation(id) ON DELETE CASCADE,
    operation_target_id  TEXT NOT NULL REFERENCES deployment_operation_target(id) ON DELETE CASCADE,
    node_id              TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    step_type            TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'pending',
    command_id           TEXT,
    task_id              TEXT,
    message              TEXT,
    started_at           TEXT,
    finished_at          TEXT
);
CREATE INDEX idx_deployment_step_operation ON deployment_step(operation_id);
CREATE INDEX idx_deployment_step_target ON deployment_step(operation_target_id);

CREATE TABLE deployment_backup (
    id            TEXT PRIMARY KEY,
    operation_id  TEXT NOT NULL REFERENCES deployment_operation(id) ON DELETE CASCADE,
    target_id     TEXT NOT NULL REFERENCES deployment_target(id) ON DELETE CASCADE,
    node_id       TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    feature_id    TEXT NOT NULL REFERENCES deployment_feature(id) ON DELETE CASCADE,
    backup_mode   TEXT NOT NULL DEFAULT 'none',
    object_key    TEXT NOT NULL,
    size          BIGINT NOT NULL DEFAULT 0,
    sha256        TEXT NOT NULL DEFAULT '',
    metadata_json TEXT,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    TEXT NOT NULL
);
CREATE INDEX idx_deployment_backup_operation ON deployment_backup(operation_id);
CREATE INDEX idx_deployment_backup_target ON deployment_backup(target_id);

CREATE TABLE bootstrap_session (
    id          TEXT PRIMARY KEY,
    node_id     TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'created',
    token_hash  TEXT NOT NULL,
    bucket      TEXT NOT NULL,
    prefix      TEXT NOT NULL DEFAULT '',
    region      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    revoked_at  TEXT,
    last_state  TEXT
);
CREATE INDEX idx_bootstrap_session_node ON bootstrap_session(node_id);
CREATE INDEX idx_bootstrap_session_status ON bootstrap_session(status);
