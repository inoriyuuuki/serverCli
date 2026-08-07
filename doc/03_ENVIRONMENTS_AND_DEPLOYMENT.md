# 环境、端口与源码部署规范

> 日期：2026-08-07  
> 本文件是当前地址和端口的权威文档。Secret 不得写入本文件。

## 1. 环境矩阵

| 环境 | 实例 | IP | 前端端口 | 后端端口 | 数据库 |
| --- | --- | --- | ---: | ---: | --- |
| 正式 | 主节点 | `<prod-server-ip>` | `9042` | `9043` | PostgreSQL |
| 测试 | 主节点 | `<test-server-ip>` | `9044` | `9045` | 本地 SQLite |
| 测试 | 子节点逻辑实例 | `<test-server-ip>` | `9046` | `9047` | 默认通过主节点 API；本机 state 可使用独立 SQLite/文件 |
| 正式 | 子节点 | 注册时确定 | 可配置 | 可配置 | 不直接连接正式 PostgreSQL |

### 1.1 端口解释

- 前端端口：浏览器访问 Web UI。
- 后端端口：REST API、节点注册、心跳、任务通道和 Lease API。
- 同一 IP 的测试主/子实例必须显式使用不同端口。
- 正式主节点后端 `9043` 与历史上可能存在的旧端口约定无关，以本文为准。

## 2. 建议 URL

| 用途 | URL 示例 |
| --- | --- |
| 正式主 UI | `https://<prod-server-ip>:9042` |
| 正式主 API | `https://<prod-server-ip>:9043` |
| 测试主 UI | `http(s)://<test-server-ip>:9044` |
| 测试主 API | `http(s)://<test-server-ip>:9045` |
| 测试子 UI | `http(s)://<test-server-ip>:9046` |
| 测试子 API | `http(s)://<test-server-ip>:9047` |

测试阶段可用受控自签 TLS，但正式必须使用受信任证书或通过受信任反向代理终止 TLS。

## 3. 环境隔离

以下内容必须分环境隔离：

- 数据库及迁移记录；
- Node Agent state；
- 节点和 AI SSH 密钥；
- 管理员密码初始化 Secret；
- TLS 证书；
- 日志、审计、任务输出和备份；
- 服务名、PID 文件、临时目录和 systemd unit；
- 前后端构建配置。

测试脚本默认只能操作测试环境。任何正式环境的部署、迁移、停止、重启、数据删除或 SSH 配置变更都必须要求显式 `--env production` 和二次确认。

## 4. 建议目录布局

```text
serverCli/
├── backend/
│   ├── cmd/control-plane/
│   ├── cmd/node-agent/
│   └── internal/
├── frontend/
├── commands/
│   ├── system-info/
│   └── service-control/
├── db/migrations/
├── deploy/
│   ├── environments/
│   └── systemd/
├── scripts/
│   ├── start.sh
│   ├── stop.sh
│   ├── restart.sh
│   ├── status.sh
│   ├── logs.sh
│   ├── migrate.sh
│   └── bootstrap-admin.sh
└── doc/
```

## 5. 源码部署流程

“一键启动”不代表跳过构建和校验。建议流程：

1. 检查操作系统、CPU 架构、磁盘空间、端口和依赖版本。
2. 加载环境配置与 Secret，禁止打印敏感值。
3. 构建前端静态资源。
4. 构建 Control Plane 和 Node Agent 二进制。
5. 校验数据库类型及连接。
6. 执行向前兼容的数据库迁移。
7. 初始化或验证管理员账号。
8. 创建 state、日志、运行目录并设置最小权限。
9. 启动后端、Agent 和前端服务。
10. 执行 readiness/health 检查。
11. 输出环境、版本、访问地址和服务状态，不输出 Secret。

## 6. 一键脚本接口草案

```bash
# 测试主实例：默认推荐入口
./scripts/start.sh --env test --role primary

# 同机测试子实例
./scripts/start.sh --env test --role child --instance test-child-1

# 正式环境：必须显式确认
./scripts/start.sh --env production --role primary --confirm-production

./scripts/status.sh --env test --all
./scripts/logs.sh --env test --role primary --follow
./scripts/restart.sh --env test --role child --instance test-child-1
./scripts/stop.sh --env test --role child --instance test-child-1
```

### 6.1 脚本行为要求

- 支持重复执行，已初始化时不重复创建管理员或节点身份。
- 失败时返回非零退出码并指出失败阶段。
- 不执行全局 Docker 清理、通配删除或不受控 `rm -rf`。
- 数据迁移前自动备份；无法备份时正式环境拒绝继续。
- 使用原子写入生成配置，权限至少为目录 `0700`、Secret 文件 `0600`。
- 脚本产生的报告必须脱敏。

## 7. 配置项草案

```dotenv
APP_ENV=test
INSTANCE_NAME=test-primary
NODE_ROLE=primary
PRIMARY_SERVER_IP=<test-server-ip>
FRONTEND_ADDR=0.0.0.0:9044
BACKEND_ADDR=0.0.0.0:9045
DATABASE_DRIVER=sqlite
DATABASE_URL=file:/var/lib/servercli/test-primary/servercli.db
AGENT_STATE_DIR=/var/lib/servercli/test-primary/agent
LOG_DIR=/var/log/servercli/test-primary
RETENTION_DAYS=7
CLEANUP_SCHEDULE=weekly
AI_LEASE_DEFAULT_MINUTES=60
AI_LEASE_MAX_HOURS=24
# 远程部署账号由运行时注入，不在仓库保存具体值
DEPLOY_SSH_USER=<runtime-secret-or-local-config>
```

正式环境应设置 `DATABASE_DRIVER=postgres`，并通过受保护的 Secret 注入 `DATABASE_URL`。仓库只提供 `.example`，不得提交真实 `.env`。

## 8. SSH 运维凭证

- 已提供的运维账号和密码仅作为部署时的带外输入，不进入 Git；脚本从受保护的本地配置或交互输入读取 `DEPLOY_SSH_USER`，密码不得落盘。
- 建议尽快迁移为运维 SSH Key，并关闭密码登录或限制其来源 IP。
- 远端自动化如需 sudo，应配置精确命令白名单，不保存 sudo 密码。
- AI Agent 临时登录不得复用运维账号密码，详见 [06_AI_SSH_LEASE.md](06_AI_SSH_LEASE.md)。

## 9. 数据库策略

### 9.1 正式 PostgreSQL

- 使用独立数据库和最小权限账号。
- 启用连接池、备份、恢复演练和磁盘容量告警。
- Control Plane 是唯一业务写入口；子节点不直接暴露数据库连接。

### 9.2 测试 SQLite

- 使用独立文件，建议启用 WAL、foreign_keys 和 busy_timeout。
- 每个逻辑实例有独立本地文件/state，避免同机文件锁冲突。
- 不以 SQLite 的并发和性能结果代表正式 PostgreSQL。
- 数据迁移必须同时在 SQLite 和 PostgreSQL 上执行测试。

## 10. 健康检查

| 端点 | 用途 |
| --- | --- |
| `/health/live` | 进程是否存活，不执行昂贵检查 |
| `/health/ready` | 数据库、迁移、关键后台任务是否可用 |
| `/version` | 版本、构建时间、commit SHA、数据库迁移版本 |

一键启动只有在 readiness 成功、前端可加载、主 Agent 心跳正常后才返回成功。
