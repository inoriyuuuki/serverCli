-- 0002_node_enrollment_instance_name: 注册申请携带逻辑实例名（如 test-child-1）
-- 兼容 SQLite 与 PostgreSQL：ALTER TABLE ... ADD COLUMN 均支持。
ALTER TABLE node_enrollment ADD COLUMN instance_name TEXT NOT NULL DEFAULT '';
