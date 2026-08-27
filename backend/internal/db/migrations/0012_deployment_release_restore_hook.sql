-- ServerCLI: 0012 — deployment_release 增加 restore_hook
-- 目的：Release 声明 restore 入口 hook（与 install/update/backup/health/rollback 并列）。
-- 兼容 SQLite 与 PostgreSQL。
ALTER TABLE deployment_release ADD COLUMN restore_hook TEXT NOT NULL DEFAULT '';
