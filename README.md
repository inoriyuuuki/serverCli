# ServerCLI — 服务器集群控制管理系统

ServerCLI 是「一台固定主服务器 + 多台节点服务器」的轻量级集群控制管理系统：

- **主节点（Control Plane）**：提供集群级 Web 管理、节点发现与审批、命令调度、统一审计、AI Agent 临时 SSH 凭证（Lease）管理。
- **子节点（Node Agent）**：主动向主节点注册，只声明并执行本机已注册命令，不依赖主节点直接 SSH 到子节点。

核心设计目标：**主节点不持有节点长期密码/私钥**；AI 只能通过临时公钥租约（默认 1 小时、累计最多 24 小时）访问受控命令。

## 功能特性

- 🌐 **Web 管理界面**：节点总览、服务器详情、命令中心、任务管理、AI 凭证（Lease）、审计日志、设置
- 🤖 **节点发现与审批**：子节点主动申请注册，主节点审批后签发不可变 `node_id` 与节点凭证
- 📡 **命令调度**：节点本地声明命令 manifest（YAML + JSON Schema 参数校验），主节点发现后远程调用，参数以 argv 传递、禁止拼接 Shell
- ⏱️ **任务系统**：长轮询领取任务、事件流上报、超时/取消/幂等（`Idempotency-Key`）
- 🔑 **AI SSH Lease**：临时公钥授权、续期、心跳、断连/撤销，全流程审计
- 🛡️ **安全与审计**：Secret 脱敏落盘、管理员会话 + CSRF、Agent 签名（HMAC）、限流、保留策略（默认 7 天）

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go（两个二进制：`servercli-control-plane` + `servercli-node-agent`） |
| 前端 | React + TypeScript + Vite（构建产物 `frontend/dist` 由控制面托管） |
| 数据库 | 正式 PostgreSQL / 测试 SQLite（WAL），共享迁移 |
| 运维 | GitHub Actions 二进制构建（物理主机直接部署）、systemd 单元、一键运维脚本，**不依赖 Docker** |
| 通信 | HTTPS REST + 节点长轮询任务通道 |

## 仓库结构

```text
serverCli/
├── backend/             # Go 后端（control-plane + node-agent + 内嵌迁移 + 测试）
├── frontend/            # React + Vite 管理界面
├── commands/            # 命令 manifest（YAML）+ 可执行文件样例
├── db/migrations/       # SQL 迁移参考目录（权威迁移嵌入 backend）
├── .github/workflows/
│   ├── build-binaries.yml        # 二进制构建 + GitHub Release（主用）
│   └── docker-build.yml.disabled # 旧 Docker 镜像构建（已停用）
├── deploy/
│   ├── environments/    # 各环境/实例 .env.example 与 .secrets.env.example
│   ├── systemd/         # control-plane / node-agent systemd 模板
│   └── docker/          # 遗留 Docker Compose 模板（已停用，仅作参考）
├── scripts/             # start/stop/restart/status/logs/migrate/bootstrap-admin/smoke-test
├── doc/                 # 需求/架构/契约文档（权威）
├── Makefile             # 本地构建 / 测试 / 冒烟入口
├── Dockerfile           # 遗留多阶段镜像（已停用，仅作参考）
└── start.sh             # 生产实例启动器（兼容 init 仓库调用模式）
```

## 环境要求

- macOS 或 Linux（bash 3.2+）
- [Go](https://go.dev/dl/)（构建后端两个二进制）
- Node.js + npm（构建前端，`npm run build`）
- curl（健康检查/冒烟测试）
- python3（冒烟测试 JSON 解析；无 jq 时必选，推荐同时安装 jq）
- 可选：`lsof` / `ss` / `netstat`（端口占用检查，至少其一）

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

# 5) 端到端冒烟测试（启动主+子 → 注册/审批 → 心跳 → 命令 → 任务 → Lease → 审计）
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

./scripts/stop.sh    --env production --role primary --confirm-production
./scripts/restart.sh --env production --role primary --confirm-production
```

正式环境部署要点：

1. 从 `deploy/environments/production/production-primary.env.example` 复制非 Secret 配置；
2. 在部署机创建 `production-primary.secrets.env`（**权限 0600**）填写 `DATABASE_URL`（PostgreSQL）与 `ADMIN_INITIAL_PASSWORD`；
3. 正式使用受信任 TLS（反向代理或证书），禁止 `HTTP_INSECURE_SKIP_VERIFY=true`；
4. systemd 模板见 `deploy/systemd/`，占位符 `<ENV>/<INSTANCE>`；
5. 若由 init 仓库托管，使用根目录 `start.sh`（读取 `.instance` 判定本机实例并启动，node-agent 以 `servercli-ai` 用户运行）。

## 二进制发布（GitHub Actions）

本项目需要直接管理物理主机，**不采用 Docker**。编译产物由 GitHub Actions
（`.github/workflows/build-binaries.yml`）自动构建，产出纯 Go 静态二进制
（`CGO_ENABLED=0`，无需在目标机安装 Go/Node）。

### 触发方式

- **推送 tag**（如 `v1.0.0`，`nightly*` 除外）：自动交叉编译默认平台
  `linux/amd64,linux/arm64`，并把每个平台的 tar.gz 包附加到 GitHub Release；
- **手动触发**（Actions 页面 `workflow_dispatch`）：可指定 `tag` 与 `platforms`
  （逗号分隔的 `os/arch`，如 `linux/amd64,linux/arm64,darwin/arm64`），
  产物仅上传为 Actions Artifact，不发布 Release。

### 产物内容（每平台一个 `servercli-<TAG>-<os>-<arch>.tar.gz`）

```text
servercli-<TAG>-<os>-<arch>/
├── bin/                  # servercli-control-plane + servercli-node-agent（静态二进制）
├── frontend/dist/        # 前端静态资源（由控制面托管）
├── commands/             # 命令 manifest 与可执行文件
├── deploy/
│   ├── environments/     # 各环境 .env.example / .secrets.env.example
│   ├── systemd/          # systemd 服务模板
│   └── servercli-lease-shell.sh
├── scripts/              # start/stop/status/migrate/bootstrap-admin 等运维脚本
├── start.sh / check.sh
├── VERSION
└── INSTALL.md            # 物理主机安装步骤（解压到 /opt/servercli + systemd）
```

### 物理主机安装（概要）

```bash
# 1) 从 GitHub Release 下载对应平台的 tar.gz 并解压
sudo mkdir -p /opt/servercli
sudo tar -xzf servercli-<TAG>-linux-amd64.tar.gz -C /opt/servercli --strip-components=1

# 2) 按 deploy/environments/ 示例准备实例配置（Secret 文件权限 0600）

# 3) 按 deploy/systemd/ 模板安装并启动服务
sudo systemctl enable --now servercli-control-plane@production-production-primary.service
sudo systemctl enable --now servercli-node-agent@production-production-primary.service
```

> 旧 Docker 镜像构建工作流已停用（`.github/workflows/docker-build.yml.disabled`），
> `Dockerfile` 与 `deploy/docker/` 仅作历史参考，不再作为部署方式。

## 脚本清单

| 脚本 | 说明 |
| --- | --- |
| `scripts/start.sh` | 一键启动：构建 → 迁移 → 管理员初始化 → 启动 → 健康检查 |
| `scripts/stop.sh` | 优雅停止（SIGTERM → 等待 → SIGKILL 兜底），清理 PID 文件；无参数即停止全部实例 |
| `scripts/restart.sh` | 停止 + 启动 |
| `scripts/status.sh` | 实例 PID / 健康 / 端口 / 数据库类型；`--all` 列出全部 |
| `scripts/logs.sh` | 查看实例日志；`--follow` 持续跟踪，`--lines N` 控制行数 |
| `scripts/migrate.sh` | 仅执行数据库迁移（迁移前自动备份） |
| `scripts/bootstrap-admin.sh` | 管理员初始化/验证（交互式或从 Secret 注入，幂等） |
| `scripts/smoke-test.sh` | 测试环境端到端冒烟，输出 PASS/FAIL，失败非零退出 |

通用参数：`--env <test|production>`（默认 test）、`--role <primary|child>`、
`--instance <NAME>`、`--confirm-production`（正式必填）、`--no-build`、
`--skip-migrate`、`--non-interactive`、`--rebuild`。

## 端口表

| 环境 | 实例 | 前端 | 后端 | 数据库 |
| --- | --- | ---: | ---: | --- |
| 正式 | 主 | `9042` | `9043` | PostgreSQL |
| 测试 | 主 | `9044` | `9045` | SQLite |
| 测试 | 子 | `9046` | `9047` | 本机独立 SQLite |

健康端点：`GET /health/live`、`GET /health/ready`、`GET /version`；
业务 API 前缀 `/api/v1`（详见 `doc/11_IMPLEMENTATION_CONTRACT.md` §4）。

## 开发与测试

```bash
make backend        # 构建后端两个二进制到 bin/
make frontend       # 构建前端静态资源
make build          # 后端 + 前端
make test           # 后端单元/集成测试（cd backend && go test ./...）
make smoke          # 端到端冒烟测试（测试主+子）
make test-primary   # 等价于 start.sh --env test --role primary
make test-child     # 等价于 start.sh --env test --role child --instance test-child-1
```

常用运维命令：

```bash
./scripts/status.sh --env test --all                                  # 查看测试主+子状态
./scripts/logs.sh --env test --role child --instance test-child-1 --follow
./scripts/stop.sh                                                     # 停止全部已启动实例
./scripts/stop.sh --env production --confirm-production               # 停止全部（含正式环境，需确认）
./scripts/stop.sh --env test --role child --instance test-child-1     # 只停指定实例
./scripts/restart.sh --env test --role primary
./scripts/migrate.sh --env test --role child --instance test-child-1
./scripts/bootstrap-admin.sh --env test --role primary --non-interactive
./scripts/smoke-test.sh --skip-start --keep-running                   # 复用已启动实例
```

## 安全说明（重要）

- **Secret 不入库**：管理员初始密码、数据库密码等只允许放在部署机
  `deploy/environments/<env>/<instance>.secrets.env`（权限 **0600**）、0600 密码文件，
  或交互式输入/进程级环境变量；`.gitignore` 已忽略 `.env`/`.secrets.env`，请勿 `-f` 强制提交。
- 所有脚本**不把密码写入日志、命令行参数或 Git**；报告统一脱敏。
- 正式环境所有写操作（部署/迁移/停止/重启/清理）必须 `--env production --confirm-production`；
  `scripts/smoke-test.sh` 只允许在测试环境运行。
- 迁移前自动备份；无法备份时正式环境拒绝继续。
- `state/`、`logs/`、`run/` 目录权限 0700；systemd 部署建议配合 `deploy/systemd/` 加固
  （`ProtectSystem=strict`、`NoNewPrivileges=true` 等）。
- 节点身份、AI 临时公钥等凭证由后端按契约管理，本仓库不含任何真实凭证。
- AI SSH 使用临时公钥租约（默认 1 小时，可续期，累计最多 24 小时），不向 AI 暴露长期密码或长期私钥。

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [doc/01_REQUIREMENTS.md](doc/01_REQUIREMENTS.md) | 业务目标、角色、功能与非功能需求、验收标准 |
| [doc/02_ARCHITECTURE.md](doc/02_ARCHITECTURE.md) | 总体架构、主子节点关系、组件边界和关键流程 |
| [doc/03_ENVIRONMENTS_AND_DEPLOYMENT.md](doc/03_ENVIRONMENTS_AND_DEPLOYMENT.md) | 正式/测试环境端口、数据库、源码部署和一键脚本要求 |
| [doc/04_DATA_MODEL.md](doc/04_DATA_MODEL.md) | PostgreSQL/SQLite 通用数据模型、状态和保留策略 |
| [doc/05_API_AND_COMMAND_PROTOCOL.md](doc/05_API_AND_COMMAND_PROTOCOL.md) | API 草案、节点通信、命令发现与远程执行协议 |
| [doc/06_AI_SSH_LEASE.md](doc/06_AI_SSH_LEASE.md) | AI Agent 临时 SSH 凭证申请、续期、撤销和审计设计 |
| [doc/07_UI_AND_ACCESS.md](doc/07_UI_AND_ACCESS.md) | 页面信息架构、主/子节点可见范围和账号权限 |
| [doc/08_SECURITY_AND_AUDIT.md](doc/08_SECURITY_AND_AUDIT.md) | 安全基线、Secret 管理、命令与 SSH 审计、日志清理 |
| [doc/09_IMPLEMENTATION_PLAN.md](doc/09_IMPLEMENTATION_PLAN.md) | 分阶段实施、测试策略、交付物与验收门禁 |
| [doc/10_CHANGE_TARGETS.md](doc/10_CHANGE_TARGETS.md) | 本轮确认需求、目标变动、待确认事项和变更记录 |
| [doc/11_IMPLEMENTATION_CONTRACT.md](doc/11_IMPLEMENTATION_CONTRACT.md) | 接口契约（权威）：目录、环境变量、API 端点、命令 Manifest 格式 |
