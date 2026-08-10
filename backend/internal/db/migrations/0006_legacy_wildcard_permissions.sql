-- ServerCLI: 0006 — 历史通配权限显式化
-- 目的：0005 之前创建的 Access Token 创建即获得全权限，权限 JSON 为 canonical
-- wildcard（{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{}}]}
-- 或等价的不带 constraints 形态）。本次权限语义改为精确匹配 + 静态权限
-- Catalog，历史 wildcard 必须显式化为旧 AI 凭证权限。
-- 安全性：
--   * 仅精确匹配上述两种历史 canonical wildcard JSON；不覆盖非 canonical、
--     手工修改或任何其他形态的 JSON。
--   * 展开结果只包含旧 AI 凭证资源（nodes:read、ai.lease_requests:create/read、
--     ai.leases:renew/heartbeat/disconnect），绝不授予 notifications 或未来资源。
--   * 不新增列（permission_version/permissions_json 已存在于 0005）。
-- 兼容 SQLite 与 PostgreSQL 的纯 SQL。

UPDATE api_access_token SET permissions_json = '{"version":1,"grants":[{"resource":"nodes","actions":["read"]},{"resource":"ai.lease_requests","actions":["create","read"]},{"resource":"ai.leases","actions":["renew","heartbeat","disconnect"]}]}'
WHERE permissions_json = '{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{}}]}'
   OR permissions_json = '{"version":1,"grants":[{"resource":"*","actions":["*"]}]}';
