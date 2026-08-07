# ServerCLI — 服务器集群控制管理系统

ServerCLI 是"一台固定主服务器 + 多台节点服务器"的轻量级集群控制管理系统：主节点提供集群级
Web 管理、节点发现、命令调度、审计和 AI Agent 临时 SSH 凭证管理；子节点只展示和管理本机能力。

- 后端：Go（`servercli-control-plane` + `servercli-node-agent`），正式 PostgreSQL / 测试 SQLite
- 前端：React + TypeScript + Vite（由控制面托管 `frontend/dist`）
- 运维：从源码一键构建/迁移/启动/健康检查脚本
- 权威契约见 [`doc/11_IMPLEMENTATION_CONTRACT.md`](doc/11_IMPLEMENTATION_CONTRACT.md)

## 仓库结构

```text
serverCli/
├── backend/             # Go 后端（控制面 + Node Agent + 内嵌迁移 + 测试）
├── frontend/            # React + Vite 管理界面
├── commands/            # 命令 manifest（YAML）+ 可执行文件样例
├── db/migrations/       # SQL 迁移参考目录（权威迁移嵌入 backend）
├── deploy/              # 环境配置模板 + systemd 单元
├── scripts/             # start/stop/restart/status/logs/migrate/bootstrap-admin/smoke-test
└── doc/                 # 需求/架构/契约文档
```

## 环境要求

- macOS 或 Linux（bash 3.2+）
- [Go](https://go.dev/dl/)（构建后端两个二进制）
- Node.js + npm（构建前端，`npm run build`）
- curl（健康检查/冒烟测试）
- python3（冒烟测试 JSON 解析；无 jq 时必选，推荐同时安装 jq）
- 可选：`lsof` / `ss` / `netstat`（端口占用检查，至少其一）

## 目录说明

```text
serverCli/
├── commands/            # 命令 manifest（YAML）+ 可执行文件样例
├── db/migrations/       # SQL 迁移参考目录（权威迁移嵌入 backend）
├── deploy/
│   ├── environments/    # 各环境/实例 .env.example 与 .secrets.env.example
│   └── systemd/         # control-plane / node-agent systemd 模板
├── scripts/             # 一键运维脚本（start/stop/restart/status/logs/migrate/bootstrap-admin/smoke-test）
├── doc/                 # 权威文档（其它组件维护）
├── backend/             # Go 源码（其它组件维护）
└── frontend/            # React + Vite（其它组件维护）
```

## 快速开始（测试环境一键启动）

```bash
# 1) 准备测试主实例配置（首次自动从 .example 生成，可按需修改）
./scripts/start.sh --env test --role primary
#    -> 前端 http://<test-server-ip>:9044  后端 http://<test-server-ip>:9045

# 2) 同机启动测试子逻辑实例（端口/state/日志独立）
./scripts/start.sh --env test --role child --instance test-child-1
#    -> 前端 http://<test-server-ip>:9046  后端 http://<test-server-ip>:9047

# 3) 查看状态 / 日志
./scripts/status.sh --env test --all
./scripts/logs.sh --env test --role primary --follow

# 4) 管理员初始化（若 secrets 未提供密码则交互式输入）
./scripts/bootstrap-admin.sh --env test --role primary

# 5) 端到端冒烟测试（启动主+子 → 注册/审批 → 心跳 → 命令 → 任务 → Lease → 审计 → 清理 dry-run）
./scripts/smoke-test.sh
```

> 首次启动会执行：依赖/端口检查 → 构建前端 → 构建后端（`bin/`）→ 数据库迁移 →
> 管理员初始化/验证 → 创建 `state/`、`logs/`、`run/`（0700）→ 启动控制面与 node-agent →
> `/health/live`、`/health/ready`、`/version` 健康检查。

## 正式环境启动（必须二次确认）

```bash
# 正式环境所有写操作必须显式 --env production --confirm-production，否则脚本拒绝
./scripts/start.sh --env production --role primary --confirm-production

# 迁移（迁移前自动备份；pg_dump 不可用或备份失败时拒绝继续）
./scripts/migrate.sh --env production --role primary --confirm-production

./scripts/stop.sh   --env production --role primary --confirm-production
./scripts/restart.sh --env production --role primary --confirm-production
```

正式环境部署要点：

1. 从 `deploy/environments/production-primary.env.example` 复制非 Secret 配置；
2. 在部署机创建 `production-primary.secrets.env`（**权限 0600**）填写
   `DATABASE_URL`（PostgreSQL）与 `ADMIN_INITIAL_PASSWORD`；
3. 正式使用受信任 TLS（反向代理或证书），禁止 `HTTP_INSECURE_SKIP_VERIFY=true`；
4. systemd 模板见 `deploy/systemd/`，占位符 `<ENV>/<INSTANCE>`。

## 脚本清单

| 脚本 | 说明 |
| --- | --- |
| `scripts/start.sh` | 一键启动：构建 → 迁移 → 管理员初始化 → 启动 → 健康检查 |
| `scripts/stop.sh` | 优雅停止（SIGTERM → 等待 → SIGKILL 兜底），清理 PID 文件 |
| `scripts/restart.sh` | 停止 + 启动 |
| `scripts/status.sh` | 实例 PID / 健康 / 端口 / 数据库类型；`--all` 列出全部 |
| `scripts/logs.sh` | 查看实例日志；`--follow` 持续跟踪，`--lines N` 控制行数 |
| `scripts/migrate.sh` | 仅执行数据库迁移（迁移前自动备份） |
| `scripts/bootstrap-admin.sh` | 管理员初始化/验证（交互式或从 Secret 注入，幂等） |
| `scripts/smoke-test.sh` | 测试环境端到端冒烟，输出 PASS/FAIL，失败非零退出 |

通用参数：`--env <test|production>`（默认 test）、`--role <primary|child>`、
`--instance <NAME>`、`--confirm-production`（正式必填）。

## API / 端口表

| 环境 | 实例 | IP | 前端 | 后端 | 数据库 |
| --- | --- | --- | ---: | ---: | --- |
| 正式 | 主 | `<prod-server-ip>` | `9042` | `9043` | PostgreSQL |
| 测试 | 主 | `<test-server-ip>` | `9044` | `9045` | SQLite |
| 测试 | 子 | `<test-server-ip>` | `9046` | `9047` | 本机独立 SQLite |

健康端点：`GET /health/live`、`GET /health/ready`、`GET /version`；
业务 API 前缀 `/api/v1`（详见 `doc/11_IMPLEMENTATION_CONTRACT.md` §4）。

## 常见命令

```bash
./scripts/status.sh --env test --all                     # 查看测试主+子状态
./scripts/logs.sh --env test --role child --instance test-child-1 --follow
./scripts/restart.sh --env test --role primary
./scripts/migrate.sh --env test --role child --instance test-child-1
./scripts/bootstrap-admin.sh --env test --role primary --non-interactive
./scripts/smoke-test.sh --skip-start --keep-running      # 复用已启动实例，测试后不停止
make test-primary                                         # 等价于 start.sh --env test --role primary
```

## 安全说明（重要）

- **Secret 不入库**：管理员初始密码、数据库密码等只允许放在部署机
  `deploy/environments/<env>/<instance>.secrets.env`（权限 **0600**）、0600 密码文件，
  或交互式输入/进程级环境变量；`.gitignore` 已忽略 `.env`/`.secrets.env`，请勿 `-f` 强制提交。
- 所有脚本**不把密码写入日志、命令行参数或 Git**；报告统一脱敏。
- 正式环境所有写操作（部署/迁移/停止/重启/清理）必须 `--env production --confirm-production`；
  `scripts/smoke-test.sh` 只允许在测试环境运行。
- 迁移前自动备份；无法备份时正式环境拒绝继续。
- state/、logs/、run/ 目录权限 0700；系统化部署建议配合 `deploy/systemd/` 加固
  （`ProtectSystem=strict`、`NoNewPrivileges=true` 等）。
- 节点身份、AI 临时公钥等凭证由后端按契约管理，本仓库不含任何真实凭证。
