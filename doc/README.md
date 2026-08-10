# ServerCLI 文档索引

> 文档基线日期：2026-08-07  
> 当前阶段：需求与架构设计，尚未开始恢复或编写业务代码。

ServerCLI 是一个“一台固定主服务器 + 多台节点服务器”的轻量级服务器集群控制管理系统。主节点提供集群级 Web 管理、节点发现、命令调度、审计和 AI Agent 临时 SSH 凭证管理；子节点只展示和管理本机能力。

## 文档列表

| 文档 | 内容 |
| --- | --- |
| [01_REQUIREMENTS.md](01_REQUIREMENTS.md) | 业务目标、角色、功能与非功能需求、验收标准 |
| [02_ARCHITECTURE.md](02_ARCHITECTURE.md) | 总体架构、主子节点关系、组件边界和关键流程 |
| [03_ENVIRONMENTS_AND_DEPLOYMENT.md](03_ENVIRONMENTS_AND_DEPLOYMENT.md) | 正式/测试环境端口、数据库、源码部署和一键脚本要求 |
| [04_DATA_MODEL.md](04_DATA_MODEL.md) | PostgreSQL/SQLite 通用数据模型、状态和保留策略 |
| [05_API_AND_COMMAND_PROTOCOL.md](05_API_AND_COMMAND_PROTOCOL.md) | API 草案、节点通信、命令发现与远程执行协议 |
| [06_AI_SSH_LEASE.md](06_AI_SSH_LEASE.md) | AI Agent 临时 SSH 凭证申请、续期、撤销和审计设计 |
| [07_UI_AND_ACCESS.md](07_UI_AND_ACCESS.md) | 页面信息架构、主/子节点可见范围和账号权限 |
| [08_SECURITY_AND_AUDIT.md](08_SECURITY_AND_AUDIT.md) | 安全基线、Secret 管理、命令与 SSH 审计、日志清理 |
| [09_IMPLEMENTATION_PLAN.md](09_IMPLEMENTATION_PLAN.md) | 分阶段实施、测试策略、交付物与验收门禁 |
| [10_CHANGE_TARGETS.md](10_CHANGE_TARGETS.md) | 本轮确认需求、目标变动、待确认事项和变更记录 |

## 当前确认的核心约束

1. 一个环境只有一个固定主节点，不做自动选主或主节点故障转移。
2. 节点通过主节点地址主动申请注册；主节点分配不可变 `node_id`。
3. IP 用于连接和展示，不作为节点唯一主键。测试环境主、子实例共用 `<test-server-ip>`，必须通过 `node_id + role + 端口` 区分。
4. 正式环境前端 `9042`、后端 `9043`；测试主节点前端 `9044`、后端 `9045`；测试子节点前端 `9046`、后端 `9047`。
5. 正式数据库使用 PostgreSQL，测试数据库使用本地 SQLite。
6. 项目从源代码构建并运行，提供一键启动、停止、状态检查和初始化脚本。
7. 管理端先实现一个简单的单管理员账号机制。
8. 命令只能由节点本地声明和封装，主节点发现后远程调用；首版不允许 Web 端提交任意 Shell。
9. AI SSH 使用临时公钥租约，不向 AI 暴露服务器长期密码或长期私钥。
10. AI 租约默认 1 小时，可自动续期，但从首次签发起累计最多 24 小时；断开、撤销或到达上限后失效。
11. AI 自助 API 一律以 Access Token（`sct_*`，库中仅存哈希与前缀）鉴权并自动审批；Lease 有效期 = `min(申请时长, Token 到期, 绝对上限)`，Token 到期/撤销后关联 Lease 立即失效并删除节点公钥。
12. 审计及过期数据默认保留 7 天，可配置；标记为重要的数据不自动删除。

## Secret 处理声明

用户提供的 SSH 登录密码属于敏感信息，**不会写入仓库或本文档**。后续部署时只允许通过以下方式之一提供：

- 仅存在于部署机的权限为 `0600` 的环境文件；
- 系统 Secret 管理服务；
- 交互式输入或进程级临时环境变量；
- SSH Key/SSH CA，作为最终推荐方案。

任何日志、数据库、API 响应、错误信息和测试报告都不得包含密码、私钥、完整 Token 或数据库连接密码。
