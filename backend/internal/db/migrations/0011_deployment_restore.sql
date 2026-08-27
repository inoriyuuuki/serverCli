-- ServerCLI: 0011 — Restore（从备份恢复）支持
-- 目的：
--   * deployment_operation.backup_id：记录 restore 操作使用的备份（审计与追溯）
--   * deployment_operation.force_delete：restore 前是否允许删除目标已有数据
--     （默认 0：目标数据非空时 restore 直接失败，需先删数据或显式 force）
-- 兼容 SQLite 与 PostgreSQL。
ALTER TABLE deployment_operation ADD COLUMN backup_id TEXT REFERENCES deployment_backup(id) ON DELETE SET NULL;
ALTER TABLE deployment_operation ADD COLUMN force_delete INTEGER NOT NULL DEFAULT 0;
