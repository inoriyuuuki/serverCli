-- ServerCLI: 0008 — 部署节点串行约束（Deployment Node Serial）
-- 目的：保证同一节点在同一时刻最多只有一个 queued/running 的部署操作目标，
-- 使同一节点上的部署默认串行执行，避免并发部署互相覆盖（agent 状态竞争、
-- 文件/服务冲突）。
-- 实现：partial unique index，与 0007 中 uq_deployment_op_active_feature
-- （同一 feature 同时最多一个 active 操作）互补：前者按 feature 串行，
-- 本索引按 node 串行。status 离开 queued/running（如 finished、succeeded、
-- failed）后自动释放，允许该节点进入下一个部署。
-- 兼容性：SQLite 与 PostgreSQL 的 partial unique index 语法一致，
-- 0007 的 deployment_operation_target(node_id) 普通索引可覆盖本查询的过滤。
CREATE UNIQUE INDEX IF NOT EXISTS uq_deployment_optarget_active_node
    ON deployment_operation_target(node_id)
    WHERE status IN ('queued', 'running');

-- 稳定排序支持：为 operation target / step 增加 created_at 列，使
-- List...ByOperation 按创建顺序稳定返回（SQLite rowid 兜底 / PG id 兜底）。
-- SQLite 的 ALTER TABLE ADD COLUMN 带 NOT NULL 需要非空默认值；PG 同语法兼容。
ALTER TABLE deployment_operation_target ADD COLUMN created_at TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_step ADD COLUMN created_at TEXT NOT NULL DEFAULT '';
