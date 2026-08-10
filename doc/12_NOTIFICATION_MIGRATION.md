# 通知模块迁移与运维指南（旧 Flask 飞书通知 → ServerCLI 通知 API）

> 日期：2026-08-10  
> 本文件面向部署/运维：旧 Flask 服务（端口 **9103**）不在本仓库，下线步骤由运维在部署机上执行；本仓库只提供新通知模块的调用契约、回归清单与阻塞项。

## 1. 背景与目标状态

- 旧实现：独立 Flask 服务（9103）接收 `GET /notice`，直接向飞书群机器人 Webhook 发送消息；**缺失 `message` 时会发送字符串 `"None"`**，且 Webhook 已泄露。
- 新实现（本仓库）：统一由主节点后端提供服务：
  - `POST /api/v1/notifications/send`（推荐，正文放 JSON，不进 URL）；
  - `GET /notice`（迁移兼容旧调用方，`method`/`message`/`logLevel` 查询参数）。
  - 两者共用同一 `NotificationService` 与限流，均要求 `Authorization: Bearer sct_*` + 权限 `notifications:send`。
- 目标：全部通知调用方迁移到新接口，旧 9103 服务下线、端口封禁，Webhook 在飞书侧废弃重建。

## 2. 旧 Webhook 泄露处置（必须在切换前完成）

1. 登录飞书管理后台/群设置，**删除旧群机器人**并新建机器人，获取新 Webhook URL。
2. 新 URL 仅写入部署机 `deploy/environments/<env>/<instance>.secrets.env`（权限 0600）的 `NOTIFICATION_FEISHU_WEBHOOK_URL=`，**禁止提交 Git/日志/示例**；仓库 `.env.example` 只写键名与占位符 `<feishu-webhook-url>`。
3. 未配置 Webhook 时服务可启动，发送返回 `503 NOT_CONFIGURED`；`GET /notice` 返回 200/`ret=0`（outcome=failure）。
4. 切换完成后，在飞书侧确认旧机器人已删除，避免泄露的旧 URL 继续被滥用。

## 3. 调用方迁移清单与「缺失 message 不再发送 None」回归清单

新调用必须带 `Authorization: Bearer <sct_* Token>`，且该 Token 已被管理员授予 `notifications:send`（新 Token 默认零权限，需先授权，见 [05_API_AND_COMMAND_PROTOCOL.md](05_API_AND_COMMAND_PROTOCOL.md) §13.4）。

> 下方按“调用方类型”给出回归项；部署前请把实际调用方（CI/CD 流水线、监控告警、部署脚本、定时任务、管理员手工脚本等）填入“调用方”列逐项核对，**重点是第 1 条**。

| # | 调用方（示例/占位，实际清单由运维补全） | 回归项 |
| --- | --- | --- |
| 1 | 所有原 `GET /notice` 调用方 | **缺失/空白 `message`（或 `method`）不再发送 `"None"`**：新实现返回 HTTP 200 `{"ret":0,"msg":"<安全归一化原因>"}` 且**不发送**。不得依赖“缺失也会发”；调用方应保证 `message` 非空，或接受“不发送”语义。 |
| 2 | 所有原 `GET /notice` 调用方 | 只传 `method` 不传 `message`（旧 Flask 发送 `"None"`）同样**不发送**，返回 200/`ret=0`。 |
| 3 | 所有原 `GET /notice` 调用方 | 响应判断变更：不能只看 HTTP 200 即“成功”，须看 `ret`（`1`=已发送；`0`=未发送：参数错误或 Provider 失败）；POST 接口则以 HTTP 状态码判断。 |
| 4 | 所有原调用方 | 新增鉴权：未带 Bearer Token → 401；Token 无 `notifications:send` → 403。需为每个调用方创建/复用 Token 并授权；Token 明文只在创建时返回一次。 |
| 5 | 所有原调用方 | 新增限流：默认每 Token 30 次/分钟、全局 120 次/分钟；超限 429 + `Retry-After`，调用方需按响应头退避重试。 |
| 6 | 传超长文本的调用方 | POST：`title` > 200 字符或 `message` > 8192 bytes → 400；GET `/notice`：`method` > 200 字符或 `message` > 4096 bytes → 200/`ret=0` 不发送。调用方应截断或改用 POST 并遵守上限。 |
| 7 | 使用 `logLevel` 的调用方 | 大小写与别名：`warn`/`warning` → warning，`error`/`fatal` → error，`info`/空/`debug`/其他 → info；`logLevel` 不影响发送成功与否。 |
| 8 | 传中文/特殊字符的调用方 | GET 查询参数必须正确 URL 编码（中文、换行、`&`、`=` 等）；建议含特殊字符时改用 POST。 |
| 9 | 发送敏感/长内容的调用方 | **必须改用 POST**：GET 查询参数会进入反向代理访问日志与浏览器历史；审计只记长度不记正文，但代理层不在服务端控制内。 |
| 10 | Webhook 未配置期间的调用方 | 发送返回 POST 503 / GET 200/`ret=0`（outcome=failure），调用方应视为“暂不可用”并告警，不静默吞掉。 |

## 4. GET /notice 使用警告

- `method`/`message` 会出现在反向代理访问日志、浏览器历史与 Referer 头；含密钥、内部路径、长文本或个人数据的通知**必须用 POST** `/api/v1/notifications/send`。
- `GET /notice` 仅用于兼容旧调用方；新调用一律优先 POST。

## 5. 旧 9103 服务下线运维步骤（不在本仓库，作为部署阻塞项清单）

> 以下步骤在部署机上由运维执行，命令以实际部署方式为准（systemd / supervisor / nginx 等）。**每项均为阻塞项**：未完成前不得宣布“旧服务已下线”。

| # | 阻塞项 | 说明/检查 |
| --- | --- | --- |
| 1 | 新通知 API 上线并通过回归 | 新主节点后端（正式 9043）已部署，`POST /api/v1/notifications/send` 与 `GET /notice` 冒烟通过（§3 回归清单全绿）。 |
| 2 | 停旧 9103 服务 | `systemctl stop <flask-notify>`（或 supervisor/其他进程管理）停止旧 Flask；确认进程退出。 |
| 3 | 移除反向代理 | 删除 nginx/apache 等对 9103（含 `/notice` 旧路径）的 `proxy_pass`/location 配置并 reload；避免流量仍打到旧服务。 |
| 4 | 封禁 9103 端口 | 防火墙（firewalld/iptables/ufw/安全组）拒绝入站 9103；确认 `ss -ltnp | grep 9103` 无监听。 |
| 5 | 指向新接口 | 所有调用方已切换为带 Bearer Token 调用新主节点 9043 的 `/notice` 或 `/api/v1/notifications/send`。 |
| 6 | Webhook 已重建 | 飞书侧旧机器人已删除、新 URL 仅存于部署机 0600 secrets（§2）。 |
| 7 | 回滚预案就绪 | 数据库已备份；明确回滚步骤与权限恢复方式（§6）。 |

## 6. 回滚说明

- **回滚不会恢复发布窗口中新 Token 的权限**：权限是数据库数据而非代码。回滚二进制或迁移后，发布期间新建/改权的 Token 保持其已写入的 `permissions_json`/`permission_version`；若回滚到无权限模型的旧版本，这些 Token 的鉴权语义可能不兼容。
- 必要时**人工重新授权或重建** Token：回滚后通过管理员界面/接口按静态权限目录重新授予所需权限，或删除并重建 Token（重建会生成新的明文，需重新分发给调用方）。
- **迁移 0006 幂等可重放**：`0006_legacy_wildcard_permissions.sql` 只精确匹配两种历史 canonical wildcard JSON 形态；重复执行不会改写已显式化或非 canonical 的 JSON，可安全重放。
- 回滚后建议运行权限启动扫描（`ScanInvalidPermissions`），确认无残留 wildcard / NULL / 空 / 非法 JSON，并核对审计中的权限变更记录。

## 7. 相关文档

- 接口规范：[05_API_AND_COMMAND_PROTOCOL.md](05_API_AND_COMMAND_PROTOCOL.md) §13
- 权限模型与审计：[08_SECURITY_AND_AUDIT.md](08_SECURITY_AND_AUDIT.md) §13–§16
- 数据模型与迁移 0006：[04_DATA_MODEL.md](04_DATA_MODEL.md) §3.18.1
- 部署环境变量：`deploy/environments/<env>/<instance>.env.example`（限流）与 `<instance>.secrets.env.example`（Webhook 键名）
