-- PostgreSQL 专用：将可能超过 int4 范围的字节/运行时长列扩容为 BIGINT(int8)。
-- SQLite 无需此迁移（动态类型）；本目录仅对 DATABASE_DRIVER=postgres 应用。
ALTER TABLE node_heartbeat ALTER COLUMN memory_total_bytes TYPE BIGINT;
ALTER TABLE node_heartbeat ALTER COLUMN memory_used_bytes TYPE BIGINT;
ALTER TABLE node_heartbeat ALTER COLUMN disk_total_bytes TYPE BIGINT;
ALTER TABLE node_heartbeat ALTER COLUMN disk_used_bytes TYPE BIGINT;
ALTER TABLE node_heartbeat ALTER COLUMN uptime_seconds TYPE BIGINT;
ALTER TABLE node_heartbeat ALTER COLUMN time_offset_ms TYPE BIGINT;
ALTER TABLE task_output ALTER COLUMN stdout_bytes TYPE BIGINT;
ALTER TABLE task_output ALTER COLUMN stderr_bytes TYPE BIGINT;
