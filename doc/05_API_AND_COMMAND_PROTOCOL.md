# API、节点通信与命令协议草案

> 日期：2026-08-07  
> API 前缀建议：`/api/v1`

## 1. API 通用约定

- 使用 JSON；时间使用 RFC 3339 UTC。
- 每个响应包含或返回头携带 `request_id`。
- 创建任务、注册申请和 Lease 申请支持 `Idempotency-Key`。
- 错误统一为：

```json
{
  "error": {
    "code": "NODE_OFFLINE",
    "message": "目标节点当前离线",
    "request_id": "req_...",
    "details": {}
  }
}
```

- 列表支持分页、筛选和排序。
- 管理员 API 使用安全 Cookie + CSRF；Agent API 使用节点独立认证。
- 子节点本机 API 必须从认证上下文固定 `node_id`，不能信任请求参数传入其他节点 ID。

## 2. 管理员与会话 API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/auth/login` | 登录 |
| `POST` | `/auth/logout` | 退出并撤销会话 |
| `GET` | `/auth/session` | 当前会话和环境信息 |
| `POST` | `/auth/password` | 修改密码 |

## 3. 节点 API

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/nodes` | 管理员 Session 或 Access Token | 主节点列出所有节点；子视图只返回本机 |
| `GET` | `/nodes/{node_id}` | 管理员 Session 或 Access Token | 节点详情 |
| `PATCH` | `/nodes/{node_id}` | 管理员 Session | 别名、标签、启用状态 |
| `GET` | `/node-enrollments` | 待审批申请 |
| `POST` | `/node-enrollments/{id}/approve` | 批准 |
| `POST` | `/node-enrollments/{id}/reject` | 拒绝 |
| `GET` | `/nodes/{node_id}/metrics` | 资源摘要/趋势 |
| `GET` | `/nodes/{node_id}/commands` | 命令清单 |

### 3.1 Agent 注册端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/agent/enrollments` | 首次申请 |
| `GET` | `/agent/enrollments/{id}` | 查询审批状态 |
| `POST` | `/agent/enrollments/{id}/claim` | 一次性领取身份 |
| `POST` | `/agent/heartbeat` | 心跳、状态、地址、能力摘要 |
| `POST` | `/agent/commands/snapshot` | 上报完整命令清单 |
| `GET/WS` | `/agent/tasks/channel` | 任务领取/事件通道 |
| `POST` | `/agent/tasks/{id}/events` | 上报任务事件 |
| `POST` | `/agent/leases/{id}/events` | 上报 Lease/SSH 会话事件 |

## 4. 命令 Manifest

命令由节点本地配置或插件提供。例如：

```yaml
apiVersion: servercli/v1
kind: Command
metadata:
  id: system.disk-usage
  version: 1.0.0
  category: system
  title: 磁盘使用情况
  description: 查看指定挂载点的磁盘使用率
spec:
  executable: /opt/servercli/commands/system/disk-usage
  permissionProfile: read-only
  timeoutSeconds: 15
  maxOutputBytes: 262144
  concurrency: 2
  parameters:
    type: object
    additionalProperties: false
    required: [mount]
    properties:
      mount:
        type: string
        enum: ["/", "/data"]
```

### 4.1 Manifest 要求

- `command_id` 在节点内稳定，版本遵循语义化版本。
- 可执行文件路径必须是绝对路径并位于允许目录。
- 参数 Schema 默认 `additionalProperties: false`。
- 禁止 Manifest 中出现 Secret。
- Manifest 和可执行文件可记录 hash；Agent 启动时校验权限和所有者。
- 升级命令后保留旧任务引用的版本信息，但不一定继续允许执行旧版本。

## 5. 创建任务

```http
POST /api/v1/nodes/{node_id}/tasks
Idempotency-Key: ai-or-admin-generated-key
```

```json
{
  "command_id": "system.disk-usage",
  "command_version": "1.0.0",
  "arguments": {
    "mount": "/"
  },
  "timeout_seconds": 15
}
```

响应：

```json
{
  "task": {
    "id": "task_...",
    "status": "queued",
    "node_id": "node_...",
    "created_at": "2026-08-07T06:00:00Z"
  }
}
```

## 6. Agent 任务载荷

Control Plane 下发内容至少包含：

- `task_id`
- `node_id`
- `command_id/command_version`
- `arguments`
- `created_at/not_before/deadline`
- `idempotency_key`
- `attempt`
- `payload_hash/signature`

Agent 必须校验目标节点、签名、时间窗、命令版本和是否已执行。

## 7. 任务事件

建议事件：

- `accepted`
- `started`
- `stdout_chunk`
- `stderr_chunk`
- `progress`
- `completed`
- `failed`
- `timed_out`
- `cancelled`

输出事件按序号去重，服务端可在流式展示后合并为最终输出。输出达到限制后停止上传多余内容，但任务本身可继续运行，并将 `truncated=true`。

## 8. 执行安全

1. 后端永远不把 Web 输入拼接成 Shell 字符串。
2. Agent 以 `execve(path, argv, env)` 类方式启动受控程序。
3. 可执行目录由 root 拥有，运行账号不可写。
4. 命令默认使用非 root 服务账号。
5. sudo 通过固定 wrapper 和精确 sudoers 条目执行。
6. 每个命令声明超时、输出上限、并发限制和权限级别。
7. 具有副作用的命令默认不自动重试。
8. 参数、输出、错误和环境变量经过脱敏。

## 9. AI Lease API（Access Token 自动审批）

外部 AI 自助接口全部要求 `Authorization: Bearer <sct_* Access Token>`。无 Token、Token 无效/过期/已撤销一律返回 `401`；有效 Token 的申请校验通过后直接 `approved` 并签发 Lease（自动审批），不再人工审批。

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/ai/lease-requests` | Access Token | 申请 Lease（自动审批，`Idempotency-Key` 防重） |
| `GET` | `/ai/lease-requests/{id}` | Access Token | 查询本人申请（非本人返回 404） |
| `POST` | `/ai/leases/{id}/renew` | Access Token | 续期 |
| `POST` | `/ai/leases/{id}/heartbeat` | Access Token | AI 客户端连接保活 |
| `POST` | `/ai/leases/{id}/disconnect` | Access Token | 正常断开并释放 |
| `GET` | `/ai/leases/{id}/status` | 签名运行时 Token（`X-Lease-Runtime-Token` 头） | `servercli-lease-shell` 校验 Lease 状态 |
| `POST` | `/ai/leases/{id}/revoke` | 管理员 Session | 撤销 Lease |
| `POST` | `/ai/leases/{id}/disable-renewal` | 管理员 Session | 禁止续期 |
| `POST` | `/ai/leases/{id}/protect` | 管理员 Session | 标记重要 |
| `POST` | `/ai/leases/revoke-all` | 管理员 Session | 紧急撤销（全局/节点） |
| `GET` | `/ai/leases` | 管理员 Session | 列表 |
| `GET` | `/ai/lease-requests` | 管理员 Session | 申请历史 |
| `PATCH` | `/settings/ai-access` | 管理员 Session | 全局/节点申请与续期开关 |

Access Token 管理（主节点管理员 Session）：`POST/GET /api-tokens`、`GET /api-tokens/{id}`、`POST /api-tokens/{id}/revoke`、`GET /api-tokens/{id}/usage-logs`。
全接口目录：`GET /meta/openapi`。

申请示例（携带 Token）：

```bash
curl -X POST "$PRIMARY_API/ai/lease-requests" \
  -H "Authorization: Bearer sct_<token>" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: <uuid>" \
  -d '{
    "node_selector": "node UUID、别名或明确 IP:端口",
    "public_key": "ssh-ed25519 AAAA... temporary-agent-key",
    "permission_profile": "read-only",
    "requested_duration_seconds": 3600,
    "purpose": "诊断服务状态",
    "client_request_id": "uuid"
  }'
```

`purpose`（使用原因）**必填**：缺失或空白时返回 400 `BAD_REQUEST`，申请不会签发 Lease。
响应包含 `lease_request`（含 `access_token_id/access_token_name`）与 `lease`、`host`、`ssh_port`、`user`；不再返回 `renewal_token`。
Lease 到期 = `min(申请时长, Token 到期时间, 系统绝对上限)`；Token 到期/撤销后 API 立即失败、关联活动 Lease 撤销并通知节点删除公钥。

IP 选择器如匹配多个同 IP 实例，API 必须返回歧义错误并要求使用 `node_id` 或 `IP:后端端口`，不得自行选择。

## 10. 权限配置建议

| Profile | 用途 | 默认能力 |
| --- | --- | --- |
| `read-only` | 诊断 | 查看系统/服务状态、读取受限日志，不修改系统 |
| `operator` | 常规维护 | 执行已批准的服务重启、部署检查等有限写操作 |
| `admin` | 高风险维护 | 默认关闭；需要人工批准、短时长和完整审计 |

Profile 映射到命令、sudoers、文件路径和网络访问白名单，而不是笼统的 Linux root 权限。

## 11. API 安全与限流

- 外部 AI 自助 API 使用 Access Token 鉴权（`sct_*`，库中仅存 SHA-256 哈希与前缀，日志/错误/示例不出现明文）；每次可识别 Token 的请求写入 `api_token_usage_log`。
- 只读节点发现接口（`GET /nodes`、`GET /nodes/{id}`）支持「管理员 Session 或 Access Token」双鉴权，便于 AI 客户端用 Token 解析 `node_id`；其余管理端写接口仍仅管理员 Session。
- 登录、Lease 申请、续期和节点注册均设置独立限流。
- 节点凭证失败达到阈值后产生高风险审计，不自动永久封禁合法节点。
- 所有错误响应不泄露节点密钥、用户存在性、内部路径和原始数据库错误。
- 正式 API 可增加来源 IP 白名单、VPN 或零信任网关。

## 12. 节点删除与历史参数 API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `DELETE` | `/nodes/{id}` | 永久删除离线/停用的子节点 `{confirm_instance_name}`；主节点与在线节点拒绝 |
| `GET` | `/task-parameter-histories` | 可复用参数历史（`node_id` 可重复、`command_id`、`command_version`、分页） |
| `DELETE` | `/task-parameter-histories/{id}` | 删除一套可选参数记录（不影响原任务） |

节点删除语义：

- 仅主节点可执行；目标必须是 `role=child` 且状态为 `offline` 或 `disabled`。
- 确认文本必须与节点不可变 `instance_name` 完全一致。
- 单事务级联删除任务、Lease、SSH 会话、免审批规则、参数历史、命令、心跳、地址、注册申请及该节点审计；删除动作本身写入一条不绑定 `node_id` 的审计。
- 删除后原节点凭证立即失效，重新上线需重新注册审批。

参数历史语义：

- 按 `节点 + command_id + command_version + 规范化参数哈希` 去重，重复执行累加 `use_count`。
- 完整保存参数（含密码、Token 等敏感字段），仅管理员 API 可读取。
- 删除只移除“一键回填”选项，不删除任务及其详情中的原始参数。
- 子节点控制面通过 `childProxy` 将本机的参数历史 GET/DELETE 转发到主节点的 agent 自服务端点，读取主节点权威数据。

## 13. 通知与 Token 权限 API

> 本节更新：2026-08-10  
> 通知 API 使用 Access Token（`sct_*`）鉴权，并要求权限 `notifications:send`；Token 权限模型见 [08_SECURITY_AND_AUDIT.md](08_SECURITY_AND_AUDIT.md) §13，迁移运维见 [12_NOTIFICATION_MIGRATION.md](12_NOTIFICATION_MIGRATION.md)。

### 13.1 发送通知 `POST /api/v1/notifications/send`

鉴权：`Authorization: Bearer <sct_* Access Token>` + 权限 `notifications:send`（Token 无该权限返回 403）。

请求 JSON（`DisallowUnknownFields`，额外字段一律 400；调用方无法提交 `source` 或任何 Webhook/目标 URL）：

```json
{
  "title": "部署完成",
  "message": "test-primary 已更新到 v1.2.0",
  "level": "info",
  "channel": "default"
}
```

| 字段 | 必填 | 校验 |
| --- | --- | --- |
| `title` | 是 | trim 后非空；≤ 200 Unicode 字符 |
| `message` | 是 | trim 后非空；≤ 8192 bytes |
| `level` | 否 | 默认 `info`；仅小写 `info` / `warning` / `error` |
| `channel` | 否 | 默认 `default`；首版仅支持 `default` |

`source` 由服务端固定注入为 `api.token`，外部不可提交。请求体不得包含 Webhook/目标 URL——调用方只能选择 `channel` 别名，服务端按别名解析 Provider（SSRF 防御）。

成功响应：

```json
{
  "notification": {
    "status": "sent",
    "channel": "default",
    "provider": "feishu"
  }
}
```

错误：`400 BAD_REQUEST`（参数非法）/ `401`（无 Token 或 Token 无效/过期/撤销）/ `403`（权限不足）/ `429 RATE_LIMITED`（限流，带 `Retry-After`）/ `502 UPSTREAM_ERROR`（Provider 失败）/ `503 NOT_CONFIGURED`（`NOTIFICATION_FEISHU_WEBHOOK_URL` 未配置）。

```bash
curl -X POST "$PRIMARY_API/api/v1/notifications/send" \
  -H "Authorization: Bearer sct_<token>" \
  -H "Content-Type: application/json" \
  -d '{"title":"部署完成","message":"构建成功","level":"info","channel":"default"}'
```

### 13.2 迁移兼容通知接口 `GET /notice`

> ⚠️ **安全警告**：本接口使用 GET 查询参数，method/message 会进入反向代理访问日志与浏览器历史，敏感或长内容**必须改用 POST**（§13.1）。

兼容旧 Flask 调用方，使用同一 NotificationService 与同一限流；鉴权仍为 `Authorization: Bearer <sct_*>` + `notifications:send`。

| 查询参数 | 说明 |
| --- | --- |
| `method` | 标题（对应 `title`），trim 后非空，≤ 200 字符 |
| `message` | 正文（对应 `message`），trim 后非空，≤ 4096 bytes |
| `logLevel` | 级别映射，忽略大小写：`warn`/`warning` → `warning`，`error`/`fatal` → `error`，`info`/空/`debug`/其他 → `info` |

响应语义（非鉴权类错误一律 HTTP 200，以 `ret` 区分）：

| 场景 | HTTP | 响应 | usage outcome |
| --- | --- | --- | --- |
| 成功 | 200 | `{"ret":1,"msg":"处理成功"}` | success |
| method/message 缺失或空白 | 200 | `{"ret":0,"msg":"<安全归一化原因>"}`，**不发送** | denied |
| method/message 超长 | 200 | `{"ret":0,"msg":"..."}`，**不发送** | denied |
| 参数错误（如 level/channel 非法） | 200 | `{"ret":0,"msg":"invalid request parameters"}` | denied |
| Webhook 未配置或 Provider 失败 | 200 | `{"ret":0,"msg":"notification send failed"}` | failure |
| 鉴权错误（无 Token/无效/权限不足） | 401 / 403 | 标准错误 | denied |
| 限流 | 429 | 标准错误 + `Retry-After` | denied |

示例：

```bash
curl "$PRIMARY_API/notice?method=%E9%83%A8%E7%BD%B2%E5%AE%8C%E6%88%90&message=%E6%9E%84%E5%BB%BA%E6%88%90%E5%8A%9F&logLevel=warn" \
  -H "Authorization: Bearer sct_<token>"
```

与旧 Flask 的安全不兼容点：旧实现把缺失 `message` 当作字符串 `"None"` 发送；新实现**缺失或空白 `method`/`message` 一律不发送**（HTTP 200/`ret=0`）。调用方回归要求见 [12_NOTIFICATION_MIGRATION.md](12_NOTIFICATION_MIGRATION.md) §3。

### 13.3 权限目录 `GET /api/v1/api-tokens/permissions/catalog`

管理员 Session + 主节点（子节点返回 403）。响应：

```json
{
  "categories": [
    {"category": "notifications", "label": "通知"},
    {"category": "ai_credentials", "label": "AI 凭证"}
  ],
  "permissions": []
}
```

静态权限目录共 7 项（分类/资源/动作/说明）：

| 分类 | 权限 | 说明 |
| --- | --- | --- |
| notifications | `notifications:send` | 发送通知 |
| ai_credentials | `nodes:read` | 节点发现（只读） |
| ai_credentials | `ai.lease_requests:create` | 申请 AI Lease |
| ai_credentials | `ai.lease_requests:read` | 查询本人发起的 Lease 申请 |
| ai_credentials | `ai.leases:renew` | 续期本人持有的 Lease |
| ai_credentials | `ai.leases:heartbeat` | 对本人持有的 Lease 发送心跳 |
| ai_credentials | `ai.leases:disconnect` | 断开本人持有的 Lease |

### 13.4 更新 Token 权限 `PUT /api/v1/api-tokens/{id}/permissions`

管理员 Session + CSRF + 主节点。新 Token 创建即**零权限**（`{"version":1,"grants":[]}`），权限通过本接口显式授予。

请求体（`permission_version` 必须等于当前 DB revision，否则 409）：

```json
{
  "permission_version": 3,
  "permissions": {
    "version": 1,
    "grants": [
      {"resource": "notifications", "actions": ["send"]}
    ]
  }
}
```

- `permissions.version`：权限 schema 版本（首版 = 1），与 DB `permission_version`（乐观锁 revision）是两回事。
- 成功：revision +1，返回 `{"api_token":{...}}`（含结构化 `permissions`）。
- `409`：revision 冲突（并发更新）、Token 已撤销或已过期。
- Token 列表/详情/创建/更新响应统一返回结构化 `permissions` 对象（非法 JSON fail closed 为空授权集，绝不泄露原始 JSON）。
- 权限更新写高风险审计：管理员、Token ID/前缀、修改前后 grants 与 revision；不记录明文/hash。
