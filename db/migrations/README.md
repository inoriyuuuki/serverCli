# db/migrations/ —— SQL 迁移参考目录

按实现契约（doc/11_IMPLEMENTATION_CONTRACT.md §1）约定：

- **权威迁移代码嵌入 backend**（由 backend 组件负责，SQLite 与 PostgreSQL 双兼容）。
- 本目录**保留用于 SQL 参考/评审**，不作为运行入口。
- 运行迁移请使用：`./scripts/migrate.sh --env test --role primary`
  （正式环境需 `--confirm-production`；迁移前自动备份，无法备份时正式环境拒绝继续）。

若未来需要在后端之外维护独立 SQL，请在此目录放置按时间戳命名的迁移文件，
并保持与后端嵌入式迁移的 schema_version 一致。
