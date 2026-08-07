# 实现契约（Implementation Contract）

> 日期：2026-08-07
> 本文件是并行实现各组件之间的接口契约：目录布局、二进制、环境变量、API 端点、命令 Manifest 格式。
> 实现以本文为准，冲突时以本文覆盖其他草案；Secret 不进入 Git。

## 1. 技术栈与目录

- 后端：Go（module 名 `servercli`），两个二进制：
  - `servercli-control-plane`：主控服务（REST API + 静态前端 + 调度/清理）
  - `servercli-node-agent`：节点 Agent
- 前端：React + TypeScript + Vite（构建产物输出到 `frontend/dist`）
- 数据库：正式 PostgreSQL / 测试 SQLite（WAL），共享逻辑模型与迁移
- 脚本：`scripts/*.sh`
- 命令样例：`commands/**`（YAML manifest + 可执行文件）

```text
serverCli/
├── backend/            # Go 源码（Go module）
│   ├── cmd/control-plane/
│   ├── cmd/node-agent/
│   └── internal/
├── frontend/           # React + Vite
├── commands/           # 命令 manifest 与可执行文件（sample）
├── db/migrations/      # SQL 迁移（嵌入式到后端，目录保留用于参考）
├── deploy/
│   ├── environments/   # 环境配置 .example 与运行时 .env（Secret 不入 Git）
│   └── systemd/        # systemd unit 模板
├── scripts/            # 一键脚本
└── doc/
```

## 2. 环境变量（控制面与 Agent 通用前缀）

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `APP_ENV` | `test` / `production` | `test` |
| `INSTANCE_NAME` | 实例名，如 `test-primary` | `test-primary` |
| `NODE_ROLE` | `primary` / `child` | `primary` |
| `PRIMARY_SERVER_IP` | 主服务器 IP（测试 `<test-server-ip>`） | `127.0.0.1` |
| `PRIMARY_BACKEND_URL` | 子节点向主控注册的基础 URL，如 `http://<test-server-ip>:9045` | 自动推导 |
| `FRONTEND_ADDR` | 前端监听地址 | `0.0.0.0:9044` |
| `BACKEND_ADDR` | 后端监听地址 | `0.0.0.0:9045` |
| `DATABASE_DRIVER` | `sqlite` / `postgres` | `sqlite` |
| `DATABASE_URL` | 连接串 | 见下 |
| `AGENT_STATE_DIR` | Agent state 目录（身份、密钥、授权文件） | `./state/<instance>` |
| `LOG_DIR` | 日志目录 | `./logs/<instance>` |
| `ADMIN_INITIAL_PASSWORD` | 管理员初始密码（运行时 Secret，用完即弃） | 无 |
| `ADMIN_INITIAL_PASSWORD_FILE` | 或从 0600 文件读取 | 无 |
| `AI_LEASE_DEFAULT_MINUTES` | 默认 Lease 时长 | `60` |
| `AI_LEASE_MAX_HOURS` | 绝对上限 | `24` |
| `AI_LEASE_DISCONNECT_GRACE_SECONDS` | 断连宽限 | `60` |
| `RETENTION_DAYS` | 默认保留天数 | `7` |
| `CLEANUP_SCHEDULE` | `weekly` / cron 表达式（简化：`weekly`/`daily`/`disabled`） | `weekly` |
| `HEARTBEAT_INTERVAL_SECONDS` | Agent 心跳间隔 | `30` |
| `OFFLINE_THRESHOLD_SECONDS` | 离线阈值 | `90` |
| `TASK_POLL_TIMEOUT_SECONDS` | Agent 任务长轮询超时 | `25` |
| `COMMANDS_DIR` | 本地命令 manifest/可执行目录 | `<repo>/commands` |
| `AUTHORIZED_KEYS_FILE` | AI 临时公钥授权文件（测试可指向 state 内文件） | `<state>/authorized_keys` |
| `LEASE_SHELL_BIN` | `servercli-lease-shell` 包装器路径 | `<state>/bin/servercli-lease-shell` |
| `HTTP_INSECURE_SKIP_VERIFY` | 测试环境允许自签 TLS | `false` |
| `LOG_LEVEL` | `debug/info/warn/error` | `info` |

SQLite 默认 `DATABASE_URL=file:<AGENT_STATE_DIR>/servercli.db`；PostgreSQL 使用 `postgres://...`。

## 3. 端口（权威）

| 环境 | 实例 | 前端 | 后端 |
| --- | --- | ---: | ---: |
| 正式 | 主 | 9042 | 9043 |
| 测试 | 主 | 9044 | 9045 |
| 测试 | 子 | 9046 | 9047 |

## 4. API 约定

- 前缀 `/api/v1`；JSON；时间 RFC3339 UTC；响应头 `X-Request-ID`。
- 错误统一 `{"error":{"code","message","request_id","details"}}`。
- 管理员会话：HttpOnly Cookie `servercli_session` + 写操作 `X-CSRF-Token` 头。
- Agent 认证：`Authorization: Bearer <node_credential>` + 请求体/头 `X-Agent-Signature`（HMAC-SHA256(node_credential, timestamp|path|body)）与 `X-Agent-Timestamp`（Unix 秒，容差 300s）。
- `Idempotency-Key` 头用于创建任务、注册申请、Lease 申请。

### 4.1 健康与系统
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health/live` | 存活 |
| GET | `/health/ready` | DB+迁移+后台任务 |
| GET | `/version` | 版本/构建/commit/迁移版本 |
| GET | `/api/v1/system/info` | 当前实例角色/环境/版本（登录或未登录均可返回环境标识） |

### 4.2 认证
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/login` | `{username,password}` |
| POST | `/api/v1/auth/logout` | 退出 |
| GET | `/api/v1/auth/session` | 当前会话+环境信息 |
| POST | `/api/v1/auth/password` | `{old_password,new_password}` |

### 4.3 节点
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/nodes` | 主看全部；子看本机 |
| GET | `/api/v1/nodes/{node_id}` | 详情（子越权 404） |
| PATCH | `/api/v1/nodes/{node_id}` | 别名/标签/备注/启用 |
| GET | `/api/v1/node-enrollments` | 申请列表（主） |
| POST | `/api/v1/node-enrollments/{id}/approve` | 批准 `{review_note}` |
| POST | `/api/v1/node-enrollments/{id}/reject` | 拒绝 `{review_note}` |
| GET | `/api/v1/nodes/{node_id}/metrics` | 最近心跳/趋势 |
| GET | `/api/v1/nodes/{node_id}/commands` | 命令清单 |

### 4.4 Agent
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/agent/enrollments` | 首次申请（无凭证） |
| GET | `/api/v1/agent/enrollments/{id}` | 查询状态 |
| POST | `/api/v1/agent/enrollments/{id}/claim` | 一次性领取身份（附签名证明） |
| POST | `/api/v1/agent/heartbeat` | 心跳+状态+地址+能力摘要 |
| POST | `/api/v1/agent/commands/snapshot` | 完整命令清单 |
| GET | `/api/v1/agent/tasks/poll` | 长轮询领取任务（返回 task 或空，见 4.7） |
| POST | `/api/v1/agent/tasks/{id}/events` | 上报任务事件 |
| POST | `/api/v1/agent/tasks/{id}/result` | 上报最终结果 |
| POST | `/api/v1/agent/leases/{id}/events` | 上报 Lease/SSH 会话事件 |

注册申请体：`{instance_request_id, hostname, requested_role, agent_version, os_name, os_version, arch, reported_addresses:[{address,address_type,service_port}], frontend_port, backend_port}`。
Claim 体：`{enrollment_id, proof_signature, proof_timestamp, public_key}`；成功返回 `{node_id, node_credential, instance_name}`。
Heartbeat 体：`{hostname, agent_version, os_name, os_version, arch, addresses:[...], cpu_usage_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms, summary:{}, commands_hash}`。

### 4.5 命令
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/commands` | 按节点/分类/关键字发现命令 |

### 4.6 任务
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/nodes/{node_id}/tasks` | 创建任务（Idempotency-Key） |
| GET | `/api/v1/tasks` | 列表（主全局/子本机） |
| GET | `/api/v1/tasks/{task_id}` | 详情+事件+输出 |
| POST | `/api/v1/tasks/{task_id}/cancel` | 取消 |

创建体：`{command_id, command_version, arguments, timeout_seconds}`；响应 `{task:{id,status,node_id,created_at}}`。

### 4.7 Agent 任务载荷与事件
Poll 响应（空则 `{"task": null}`）：
```json
{"task": {"task_id","node_id","command_id","command_version","arguments","created_at","not_before","deadline","idempotency_key","attempt","payload_hash","signature"}}
```
事件体：`{event_type:"accepted|started|stdout_chunk|stderr_chunk|progress|completed|failed|timed_out|cancelled", sequence, message, occurred_at}`。
Result 体：`{status:"succeeded|failed|timed_out|cancelled|result_unknown", stdout_text, stderr_text, exit_code, error_code, error_message, truncated, finished_at}`。

### 4.8 AI Lease
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/ai/lease-requests` | AI 申请（Idempotency-Key） |
| GET | `/api/v1/ai/lease-requests/{id}` | 查询 |
| POST | `/api/v1/ai/leases/{id}/renew` | 续期（AI 签名头） |
| POST | `/api/v1/ai/leases/{id}/heartbeat` | 保活 |
| POST | `/api/v1/ai/leases/{id}/disconnect` | 正常断开 |
| POST | `/api/v1/ai/leases/{id}/revoke` | 管理员撤销 `{reason, terminate_sessions}` |
| GET | `/api/v1/ai/leases` | 列表 |
| GET | `/api/v1/ai/lease-requests` | 申请历史 |
| PATCH | `/api/v1/settings/ai-access` | 开关 `{new_requests_enabled, renewals_enabled, scope:"global"|node_id}` |

申请体：`{node_selector, public_key, permission_profile:"read-only|operator|admin", requested_duration_seconds, purpose, client_request_id}`。
响应：`{lease_request:{id,status}, lease?:{id,node_id,expires_at,absolute_expires_at,...}}`。
AI 认证：`Authorization: Bearer <ai_renewal_token>`（申请成功时返回，服务端只存哈希）。

### 4.9 审计 / 设置 / 清理
| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/audit-events` | 列表+筛选（时间/节点/actor/action/result/risk/重要） |
| GET | `/api/v1/settings` | 系统设置 |
| PATCH | `/api/v1/settings` | 更新设置 |
| POST | `/api/v1/cleanup/run` | 手动清理 `{dry_run, data_types?}` |
| GET | `/api/v1/cleanup/runs` | 清理记录 |

### 4.10 节点本机 API 范围
子节点后端认证上下文固定为自身 `node_id`：`/nodes` 只返回自己；访问其他节点返回 404。

## 5. 命令 Manifest（YAML）

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
  executable: /absolute/path/to/disk-usage   # 或相对 COMMANDS_DIR
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

Agent 启动/变更时校验 manifest 与可执行文件 hash、权限，上报 `commands/snapshot`：
```json
{"commands":[{"command_id","command_version","capability_id","category","title","description","parameter_schema_json","permission_profile","timeout_seconds","max_output_bytes","enabled","manifest_hash","executable_hash"}]}
```

## 6. 数据模型
遵循 `04_DATA_MODEL.md` 的表设计；首版必须实现：`admin_user, admin_session, node_enrollment, node, node_address, node_heartbeat, node_command, task, task_event, task_output, ai_lease_request, ai_lease, ai_lease_event, ai_ssh_session, audit_event, system_setting, cleanup_run`。时间字段 UTC，主键 UUID 字符串，`is_protected/protected_at` 覆盖所有会被清理的表。Token/凭证只存哈希+前缀。迁移必须 SQLite 与 PostgreSQL 兼容（避免使用方言专有类型）。

## 7. 脚本接口

```bash
./scripts/start.sh --env test --role primary [--instance test-primary]
./scripts/start.sh --env test --role child --instance test-child-1
./scripts/stop.sh --env test --role child --instance test-child-1
./scripts/restart.sh ... ; ./scripts/status.sh --env test --all ; ./scripts/logs.sh --env test --role primary --follow
./scripts/migrate.sh --env test --role primary ; ./scripts/bootstrap-admin.sh --env test --role primary
```
- 默认测试环境；正式必须 `--env production --confirm-production`。
- 脚本从源码构建 → 迁移 → 初始化/验证管理员 → 启动 → 健康检查；失败非零退出并指出阶段。
- 配置从 `deploy/environments/<env>/<instance>.env`（非 Secret）与同目录 `<instance>.secrets.env`（0600）读取。
- 每个逻辑实例独立 state/log/pid 目录：`./state/<instance>`、`./logs/<instance>`、`./run/<instance>`。

## 8. 测试与验收基线

- 后端：关键路径单元测试 + SQLite 集成测试；`go test ./...` 通过。
- 前端：`npm run build` 通过。
- 端到端冒烟脚本 `scripts/smoke-test.sh`：启动测试主+子 → 注册/审批 → 心跳 → 命令 → 任务 → Lease 申请/续期/断开 → 审计 → 清理（dry-run）。
- 端口：主 9044/9045、子 9046/9047 健康检查通过。
