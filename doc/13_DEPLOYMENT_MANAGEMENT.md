# 部署管理模块总体设计（Deployment Management）

> 日期：2026-08-26
> 本文是 ServerCLI「部署管理」模块的权威总体设计契约。后续实现代理（后端、前端、Node Agent、Hook、安全）以本文为准。
> 冲突时以本文覆盖草案；本模块只执行节点本机能力与固定 `deployment.*` 命令，不允许 Web 提交任意 Shell。

## 1. 目标与范围

「部署管理」在多台 ServerCLI 节点服务器上完成 Feature 的安装、更新、备份与回滚：

- 以**私有 OSS 为唯一制品源**，通过 OSS Repository Sync 将权威制品同步到主控的部署根目录；
- 由主控的 Deployment Service / Planner 编排 `deployment.*` 固定命令，经**现有 Task Service** 下发到目标节点；
- 节点端 Node Agent 只负责执行固定命令并回报结果，Hook 在本机安全 Hook Runner 中运行；
- V1 的 Secret 明文存放在私有 OSS（见 `16_PLAINTEXT_OSS_SECRETS_V1.md`），加密模式在数据模型与接口中预留。

## 2. 分层架构（权威）

```text
部署管理 UI（前端）
        │
        ▼
Deployment API（/api/v1/deployments/*，admin 认证，Group「部署管理」）
        │
        ▼
Deployment Service / Planner（编排、状态机、冻结快照、审计）
        │
        ├──▶ Feature Catalog        （features/<feature_key>/manifest.yaml）
        ├──▶ Release Catalog        （releases/<feature>/<version>/<sha256>/）
        ├──▶ Config Resolver        （配置合并：Feature 默认 < Profile < 节点 Override < 派生 < Secret Binding）
        ├──▶ Repository Secret Provider（解析/校验/物化/清理 Secret，只存引用不存正文）
        └──▶ OSS Repository Sync    （私有 OSS ⇄ 部署根目录 repository/，含备份 backups/ 前缀）
        │
        ▼
Deployment Operation Scheduler（操作队列、幂等、取消、重试、继续）
        │
        ▼
现有 Task Service（任务下发/长轮询/结果回收，复用现有节点通信与鉴权）
        │
        ▼
Node Agent（固定 deployment.* 命令）
        │
        ▼
本机安全 Hook Runner（低权限 + 精确 sudo wrapper + 进程组取消）
```

- 主控是唯一编排权威；节点不自主升级、不擅自回滚。
- 所有跨节点副作用必须经过 Deployment Operation Scheduler → Task Service，禁止绕过任务通道直接调用节点。

## 3. 统一部署根目录

**权威常量：`DEPLOYMENT_ROOT_DIR=/opt/servercli-deployment`**

```text
/opt/servercli-deployment/
├── repository/                      # 与 OSS 权威同步（可被 Sync 全量/增量覆盖）
│   ├── catalog/                     # 目录/索引数据（同步而来）
│   ├── features/                    # features/<feature_key>/manifest.yaml
│   ├── releases/                    # releases/<feature>/<version>/<sha256>/{manifest.json,bundle.tar.zst,bundle.sig}
│   ├── configs/
│   │   ├── shared/                  # 共享 Config Profile（非敏感配置）
│   │   └── nodes/<node_id>/         # 节点级配置覆盖
│   ├── secrets/
│   │   ├── shared/                  # secrets/shared/<profile>.secrets.yaml
│   │   └── nodes/<node_id>/         # secrets/nodes/<node_id>/<feature>.secrets.yaml
│   └── manifests/                   # manifests/repository-manifest.json（逐对象 sha256+签名）
└── .servercli-local/                # 本地运行数据，不参与 OSS 同步
    ├── credentials/                 # 节点凭证、上传授权（0700）
    ├── runtime/                     # 运行时状态
    ├── state/                       # 本地状态（Agent/操作本地数据）
    ├── rendered/                    # 渲染后的配置/部署产物（执行前临时生成）
    ├── staging/                     # 下载/校验/解压暂存区
    └── logs/                        # 本地日志
```

### 3.1 目录规则（强制）

1. `repository/` 与 OSS 权威同步：`OSS Repository Sync` 以 OSS 为真源，同步后按仓库级 Manifest 逐对象校验（sha256 + 独立签名）；本地不产生权威差异。
2. `.servercli-local/` **不参与 OSS 同步**：不同步、不上传、不下载；其中 `credentials/` 与 `rendered/` 属敏感区。
3. `secrets/` 目录权限 `0700`，Secret 文件权限 `0600`；其他目录默认 `0755`、文件 `0644`。
4. `repository/` **不得 Git 跟踪**：禁止 `git add` 部署根目录或其中任何文件（见 14 号文档 P0 整改项）。
5. 备份对象走**独立 `backups/` 前缀**，与 `deployment-repository/` 前缀分离，使用独立的精确前缀写入凭证。
6. 所有路径均为服务端/节点端固定结构拼接，禁止接受任意相对路径或 `..` 穿越。

## 4. OSS 目录与制品布局（权威）

### 4.1 仓库前缀

```text
deployment-repository/
├── catalog/
├── features/                  # features/<feature_key>/manifest.yaml
├── releases/                  # releases/<feature>/<version>/<sha256>/
├── configs/
│   ├── shared/
│   └── nodes/
├── secrets/
│   ├── shared/
│   └── nodes/
└── manifests/                 # manifests/repository-manifest.json
```

### 4.2 Release 制品（不可变路径）

```text
releases/<feature>/<version>/<sha256>/
├── manifest.json
├── bundle.tar.zst
└── bundle.sig
```

- `<sha256>` 为 `bundle.tar.zst` 的 SHA-256（小写 hex）；路径一经发布不可变。
- `manifest.json` 内 `object_key` 必须与该路径一致；禁止把 `latest` 等浮动标签当作真实版本使用（见 15 号文档）。

### 4.3 备份对象（独立前缀）

```text
backups/<environment>/<feature>/<node_id>/<yyyy>/<mm>/<dd>/<operation_id>/
├── backup.tar.zst
└── metadata.json
```

- `<environment>` 为环境标识（如 `production` / `test`），`<operation_id>` 为部署操作的唯一 ID。
- 备份写入使用独立于仓库同步的精确前缀上传授权（见 14 号文档与 `upload-authorize`）。

## 5. 状态机（权威）

### 5.1 Bootstrap 状态机（节点纳入部署管理）

```text
created
  → repository_syncing
  → repository_verified
  → agent_downloading        # Agent 从公开 OSS 桶 inori-image 下载（不经过 xray）
  → agent_verifying
  → agent_installing
  → (xray_installing → proxy_checking → proxy_ready)   # 业务服务需访问 GitHub 时启用
  → enrollment_pending
  → node_online
  → completed
```
> 说明：V1 首次引导的 **ServerCLI Agent 制品不再经 xray 访问 GitHub 获取**。GitHub
> Actions 构建完成后将制品上传到公开读的 OSS 桶 `inori-image`，对象名**固定为
> `servercli/latest/servercli-latest-linux-<arch>.tar.gz` + `sha256sums.txt`
> （不区分版本号，始终指向最新构建）**；节点在控制面模式下从该桶经 HTTPS
> 下载并做 SHA-256 校验后安装到 `/usr/local/bin/servercli-node-agent`。
> xray 安装/探活仅保留给**业务服务**访问 GitHub 等外网资源时使用（可选阶段）。

失败终态（不可自动恢复，需人工处理）：

```text
repository_sync_failed
manifest_invalid
signature_failed
xray_failed
proxy_failed
agent_download_failed
agent_verify_failed
agent_start_failed
enrollment_failed
expired
cancelled
```

### 5.2 Operation 状态机（安装/更新/备份/回滚）

```text
draft
  → validated
  → awaiting_confirmation
  → queued
  → running
  → succeeded
```

终态（不可自动恢复，需人工处理）：

```text
partial_failed
failed
cancelled
rolled_back
rollback_failed
```

- `draft`：Planner 创建，尚未校验。
- `validated`：校验通过，已生成冻结快照，等待确认。
- `awaiting_confirmation`：已请求管理员确认。
- `queued`：已确认，进入调度队列。
- `running`：经 Task Service 下发到目标节点执行中。
- `succeeded`：全部目标成功且健康检查通过。
- `partial_failed`：部分目标成功、部分失败（按目标记录状态）。
- `failed`：全部失败或任一步骤不可重试。
- `cancelled`：取消成功（已确认取消且无副作用残留）。
- `rolled_back`：回滚完成。
- `rollback_failed`：回滚失败（进入人工介入，禁止自动重试）。

## 6. 数据模型（至少 12 张表，权威）

通用约定：时间戳 UTC；主键 `id`（UUID 或自增）；`created_at`/`updated_at` 各表均有；
`encryption_mode` 默认 `'none'`；**Secret 相关表只存 `object_key`/`version`/`hash`/`encryption_mode`/`metadata`，绝不存正文**。

### 6.1 deployment_feature

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| feature_key | text unique | Feature 标识，如 `app` / `web` |
| name | text | 展示名 |
| description | text | 描述 |
| latest_release_id | fk nullable | 当前推荐 Release |
| os | text | 目标系统 |
| arch | text | 架构 |
| source_commit | text | 源码 commit |
| minimum_agent_version | text | 最低 Agent 版本 |
| dependencies | json | 依赖 Feature 列表 |
| backup_mode | text | database_dump/application_snapshot/filesystem_quiesced/cold_backup/external_snapshot/none |
| rollback_capability | text | 支持的回滚能力描述 |
| config_schema | json | JSON Schema（必填） |
| status | text | active/disabled |
| created_at / updated_at | ts | 审计时间 |

### 6.2 deployment_release

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| feature_id | fk | 所属 Feature |
| feature_key | text | 冗余 Feature 标识 |
| version | text | 语义版本 |
| source_commit | text | 源码 commit |
| os / arch | text | 平台 |
| object_key | text | releases/<feature>/<version>/<sha256>/... |
| size | bigint | bundle 大小 |
| sha256 | text | bundle SHA-256 |
| signature | text | bundle 独立签名 |
| config_schema | json | JSON Schema |
| install_hook / update_hook / backup_hook / health_hook / rollback_hook | text | 固定相对脚本路径 |
| dependencies | json | 依赖 |
| minimum_agent_version | text | 最低 Agent 版本 |
| backup_mode | text | 备份模式 |
| data_migration_metadata | json | 数据迁移元数据 |
| status | text | published/deprecated |
| published_at | ts | 发布时间 |
| created_at / updated_at | ts | 审计时间 |

### 6.3 oss_profile

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| name | text unique | Profile 名 |
| endpoint | text | OSS Endpoint（必须命中白名单） |
| bucket | text | Bucket 名 |
| region | text | 区域 |
| repository_prefix | text | 默认 `deployment-repository/` |
| backup_prefix | text | 默认 `backups/` |
| use_https | boolean | 强制 true |
| credential_source | text | env/encrypted/external |
| credential_ref | text | 凭据引用（不存明文 AK/Secret） |
| encryption_mode | text | 默认 `'none'` |
| status | text | active/disabled |
| created_at / updated_at | ts | 审计时间 |

**注意**：OSS Credential 不落库明文（见 14/16 号文档），只允许 `credential_ref` 引用或加密存储。

### 6.4 deployment_config_profile

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| profile_key | text unique | Profile 标识（对应 configs/shared/ 下文件） |
| name | text | 展示名 |
| config | json | 非敏感共享配置（禁止含 Secret） |
| config_hash | text | 配置 SHA-256 |
| scope | text | shared（V1 仅 shared） |
| version | int | 版本号 |
| created_at / updated_at | ts | 审计时间 |

### 6.5 deployment_secret_reference

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| reference_key | text unique | 引用标识（`shared:<profile>` 或 `node:<node_id>:<feature>`） |
| object_key | text | Secret 对象 key（不存正文） |
| version | text | Secret 版本 |
| hash | text | Secret 内容 SHA-256 |
| encryption_mode | text | 默认 `'none'`（预留 aes-gcm/kms-envelope） |
| metadata | json | 附加元数据（长度、文件名等，不含正文） |
| scope | text | shared/node |
| node_id | fk nullable | node 作用域时必填 |
| feature_key | text nullable | 关联 Feature |
| created_by | text | 创建者 |
| created_at / updated_at | ts | 审计时间 |

### 6.6 deployment_target

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| node_id | fk unique | 目标节点 |
| environment_id | text | 环境标识 |
| feature_key | text | 部署的 Feature |
| desired_release_id | fk nullable | 期望 Release |
| current_release_id | fk nullable | 当前 Release |
| previous_release_id | fk nullable | 上一次 Release（回滚目标） |
| last_healthy_release_id | fk nullable | 最近健康 Release |
| target_config | json | 节点级配置覆盖 |
| config_hash | text | 合并后配置 hash（冻结用） |
| enabled | boolean | 是否允许操作 |
| state | text | 当前部署状态摘要 |
| created_at / updated_at | ts | 审计时间 |

### 6.7 deployment_target_secret

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| target_id | fk | 目标 |
| secret_reference_id | fk | 引用 deployment_secret_reference |
| object_key | text | Secret 对象 key |
| version | text | Secret 版本 |
| hash | text | Secret 内容 SHA-256 |
| encryption_mode | text | 默认 `'none'` |
| metadata | json | 元数据（不含正文） |
| created_at / updated_at | ts | 审计时间 |

### 6.8 deployment_operation

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| operation_id | text unique | 对外操作 ID |
| operation_type | text | install/update/backup/rollback |
| feature_key | text | Feature |
| release_id / release_version | fk/text | 目标 Release 冻结 |
| manifest_hash | text | Manifest hash 冻结 |
| config_snapshot | json | 配置快照冻结 |
| config_hash | text | 配置 hash 冻结 |
| secret_snapshot | json | Secret 引用快照（object_key/version/hash/encryption_mode，无正文） |
| target_set_snapshot | json | 目标节点集合冻结 |
| previous_release_id | fk nullable | 回滚基线冻结 |
| last_healthy_release_id | fk nullable | 最近健康冻结 |
| status | text | 见 Operation 状态机 |
| requested_by / confirmed_by | text | 操作人 |
| confirmed_at / queued_at / started_at / finished_at | ts | 阶段时间 |
| cancel_requested | boolean | 是否请求取消 |
| retry_count | int | 重试计数 |
| error_code | text nullable | 错误码 |
| created_at / updated_at | ts | 审计时间 |

### 6.9 deployment_operation_target

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| operation_id | fk | 操作 |
| target_id | fk | 目标 |
| node_id | fk | 节点 |
| status | text | 目标级状态（pending/running/succeeded/failed/skipped/cancelled/rolled_back） |
| current_step | text | 当前步骤 |
| previous_release_id | fk nullable | 冻结 |
| last_healthy_release_id | fk nullable | 冻结 |
| started_at / finished_at | ts | 时间 |
| error_code / result_code | text | 错误/结果码 |
| created_at / updated_at | ts | 审计时间 |

### 6.10 deployment_step

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| operation_id | fk | 操作 |
| operation_target_id | fk | 目标 |
| node_id | fk | 节点 |
| step_type | text | download/verify/unpack/install/update/backup/health-check/rollback/cleanup |
| command_name | text | 固定 `deployment.*` 命令名 |
| status | text | pending/running/succeeded/failed/cancelled |
| exit_code | int nullable | 退出码 |
| attempt | int | 尝试次数 |
| duration_ms | int | 耗时 |
| log_summary | text | 摘要（禁止 Hook stdout/正文，见审计白名单） |
| started_at / finished_at | ts | 时间 |
| created_at | ts | 创建时间 |

### 6.11 deployment_backup

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| backup_id | text unique | 对外备份 ID |
| operation_id | fk nullable | 关联操作 |
| target_id / node_id | fk | 来源目标/节点 |
| feature_key | text | Feature |
| object_key | text | backups/.../backup.tar.zst |
| size | bigint | 大小 |
| sha256 | text | 备份 SHA-256 |
| encryption_mode | text | 默认 `'none'` |
| status | text | pending/uploading/uploaded/verified/failed |
| metadata | json | 备份元数据（backup_mode、版本等） |
| expires_at | ts | 保留到期 |
| created_at / updated_at | ts | 审计时间 |

### 6.12 bootstrap_session

| 列 | 类型 | 说明 |
| --- | --- | --- |
| id | pk | 主键 |
| session_id | text unique | 对外 Session ID |
| node_id | fk nullable | 目标节点 |
| environment_id | text | 环境 |
| status | text | 见 Bootstrap 状态机 |
| current_state | text | 当前状态 |
| repo_sync_status | text | 仓库同步状态 |
| xray_status | text | Xray 状态 |
| proxy_status | text | 代理状态 |
| agent_version | text nullable | Agent 版本 |
| agent_sha256 | text nullable | Agent 校验 |
| enrollment_token_hash | text | 注册令牌哈希（不存明文） |
| expires_at | ts | 过期时间 |
| created_by | text | 创建者 |
| completed_at | ts nullable | 完成时间 |
| error_code | text nullable | 错误码 |
| created_at / updated_at | ts | 审计时间 |

## 7. 配置合并顺序（权威）

执行前由 Config Resolver 按以下优先级**从低到高**合并，生成最终配置快照：

```text
1. Feature 默认配置            （features/<feature_key>/manifest.yaml 内 config 默认值）
2. 共享 Config Profile         （configs/shared/<profile>）
3. 节点 Override               （configs/nodes/<node_id>/）
4. 系统派生字段                （node_id / environment_id / hostname / 节点地址 / feature_key /
                                release_version / deployment_root_dir / operation_id / 数据目录 / 服务端口）
5. Secret Binding              （Repository Secret Provider 物化结果，仅以引用注入，绝不进入配置全文）
```

- 高优先级覆盖低优先级；派生字段由系统计算，禁止用户配置覆盖。
- Secret Binding 不参与配置 JSON 序列化，只以文件物化 + 路径/引用方式注入 Hook 环境。

## 8. 执行前冻结（权威）

任何 Operation 从 `validated` 进入 `queued` 前，Planner **必须冻结**以下快照并写入 `deployment_operation`，执行期不得中途变更，任何变更视为校验失败：

1. Release ID
2. Manifest hash
3. 配置快照
4. 配置 hash
5. Secret reference（object_key / version / hash / encryption_mode）
6. 目标节点集合
7. previous release
8. last healthy

## 9. API 清单（权威契约）

### 9.1 约定

- 前缀：`/api/v1/deployments/*`
- API Group：`部署管理`
- 认证：管理员会话（admin）；写操作需 `X-CSRF-Token`；幂等写操作支持 `Idempotency-Key`。
- 时间 RFC3339 UTC；错误统一 `{"error":{"code","message","request_id","details"}}`。
- Agent 接口使用 Agent 认证（Bearer node_credential + 请求签名）。
- 所有响应与审计受 14/16 号文档安全约束：**不回显 Secret 正文/配置全文/OSS Credential/预签名 URL**。

### 9.2 Features

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/features` | deployments:read | 列表 |
| POST | `/api/v1/deployments/features` | deployments:configure | 创建/注册 Feature |
| GET | `/api/v1/deployments/features/{id}` | deployments:read | 详情 |

### 9.3 Releases

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/releases` | deployments:read | 列表 |
| POST | `/api/v1/deployments/releases` | deployments:configure | 发布 Release（写入 OSS 并登记） |

### 9.4 OSS Profiles

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/oss-profiles` | deployments:read | 列表 |
| POST | `/api/v1/deployments/oss-profiles` | deployments:configure | 创建 |
| PUT | `/api/v1/deployments/oss-profiles/{id}` | deployments:configure | 更新（凭据只允许引用，不回显） |
| DELETE | `/api/v1/deployments/oss-profiles/{id}` | deployments:configure | 删除 |
| POST | `/api/v1/deployments/oss-profiles/{id}/test` | deployments:configure | 连通性/权限测试 |

### 9.5 仓库同步

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/v1/deployments/repository/sync` | deployments:configure | 触发 OSS → 本地 repository/ 权威同步并校验 |

### 9.6 Config Profiles

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/config-profiles` | deployments:read | 列表 |
| POST | `/api/v1/deployments/config-profiles` | deployments:configure | 创建 |
| PUT | `/api/v1/deployments/config-profiles/{id}` | deployments:configure | 更新 |
| DELETE | `/api/v1/deployments/config-profiles/{id}` | deployments:configure | 删除 |

### 9.7 Targets

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/targets` | deployments:read | 列表 |
| POST | `/api/v1/deployments/targets` | deployments:install | 创建目标（纳入部署管理） |
| PUT | `/api/v1/deployments/targets/{id}` | deployments:install | 更新目标配置 |
| DELETE | `/api/v1/deployments/targets/{id}` | deployments:install | 移除目标 |

### 9.8 Secrets 引用

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/secrets/references` | deployment_secrets:manage | 列出引用（只含 object_key/version/hash/encryption_mode/metadata，无正文） |
| POST | `/api/v1/deployments/secrets/{id}/overwrite` | deployment_secrets:manage | 覆盖 Secret（只允许覆盖，不允许读取/回显正文；生成新 version） |

### 9.9 Operations

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/operations` | deployments:read | 列表 |
| POST | `/api/v1/deployments/operations` | deployments:install/update/backup/rollback | 创建操作（draft→validated→awaiting_confirmation） |
| GET | `/api/v1/deployments/operations/{id}` | deployments:read | 详情 |
| POST | `/api/v1/deployments/operations/{id}/cancel` | deployments:install/update/backup/rollback | 请求取消 |
| POST | `/api/v1/deployments/operations/{id}/continue` | deployments:install/update/backup/rollback | 确认后继续执行 |

### 9.10 Backups

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/backups` | deployments:backup | 列表 |
| GET | `/api/v1/deployments/backups/{id}` | deployments:backup | 详情（不含预签名 URL 查询参数） |

### 9.11 Bootstrap Sessions

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/deployments/bootstrap-sessions` | bootstrap_sessions:read | 列表 |
| POST | `/api/v1/deployments/bootstrap-sessions` | bootstrap_sessions:create | 创建纳入会话 |
| POST | `/api/v1/deployments/bootstrap-sessions/{id}/revoke` | bootstrap_sessions:revoke | 撤销 |

### 9.12 Agent 授权接口

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/v1/agent/deployments/upload-authorize` | Agent（node_credential + 签名） | 返回受限上传授权（精确前缀、短时效、单次），供节点上传备份/制品 |

- 响应仅含：`upload_method`、`upload_url`（受保护，不含可泄露查询参数）、`prefix`（精确前缀，如 `backups/<environment>/<feature>/<node_id>/<yyyy>/<mm>/<dd>/<operation_id>/`）、`expires_at`、`max_size`。
- 授权前缀由服务端按固定结构生成，节点不可自行指定任意 key。

## 10. 权限清单

| 权限 | 说明 |
| --- | --- |
| deployments:read | 查看 Feature/Release/Profile/Target/Operation/Backup |
| deployments:configure | 管理 Feature、Release、OSS Profile、Config Profile、仓库同步 |
| deployments:install | 创建/更新/移除目标，执行安装 |
| deployments:update | 执行更新 |
| deployments:backup | 执行/查看备份 |
| deployments:rollback | 执行回滚 |
| deployment_secrets:manage | 查看 Secret 引用、覆盖 Secret（不回显正文） |
| bootstrap_sessions:create / read / revoke | 创建/查看/撤销 Bootstrap 会话 |

## 11. 审计白名单（权威）

部署管理相关审计事件**只允许**写入以下字段（字段值本身同样不得包含敏感内容）：

```text
feature_key
release_version
node_id
target_id
operation_id
backup_id
config_hash
secret_reference_id
secret_version
secret_hash
encryption_mode
action
result
reason_length
```

**禁止写入审计/日志/事件/API 响应**：

```text
Secret 正文
配置全文
OSS Credential
预签名 URL
URL query
Hook stdout
Provider 响应正文
```

- `reason_length` 只记录原因的长度，不记录原因内容。
- 任何链路（数据库、Task 事件、日志、审计、通知、API 响应）命中禁止项即视为安全事件，必须被 Redactor 拦截。

## 12. V1 非目标清单（12 项，权威）

1. 不做多主/高可用/故障转移的部署控制面；部署编排仍为单主权威。
2. 不做灰度/金丝雀/分批自动推进与自动回滚编排；回滚为人工确认的整批操作。
3. 不做零停机滚动发布或蓝绿切换；安装/更新允许短暂服务中断。
4. 不提供 Web 任意脚本/任意 Shell 执行；只执行 Feature Bundle 内固定 `deployment.*` 命令。
5. 不自动发现公网/多云节点；目标节点必须先通过现有节点注册流程纳入。
6. 不支持非 OSS 的制品仓库；私有 OSS 是唯一制品源。
7. 不支持 Kubernetes/容器编排/大规模集群调度。
8. 不支持多租户/组织级 RBAC；沿用单管理员 + 权限位模型。
9. V1 Secret 明文存储在私有 OSS（加密模式在数据模型/接口预留，不在 V1 强制启用）。
10. 不做配置热加载/运行时动态生效；任何变更必须经过完整 Operation 流程。
11. 不做浏览器内交互式终端或实时日志流。
12. 不持久化完整 Hook stdout 与 Provider 响应正文；只保留摘要、退出码与 reason 长度。
