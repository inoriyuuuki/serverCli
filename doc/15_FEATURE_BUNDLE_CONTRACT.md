# Feature Bundle 契约（发布格式）

> 日期：2026-08-26
> 本文定义「部署管理」Feature Bundle 的权威发布格式：Feature Manifest、Release 制品、仓库级 Manifest、
> Secret 文件命名与配置 Schema。发布方（构建/CI）与消费方（主控、Node Agent、Hook Runner）均以本文对齐。
> 制品路径/命名不可变，禁止浮动标签。

## 1. 总览

- 每个 Feature 有唯一的 `feature_key`（如 `app` / `web` / `monitor`）。
- 每个 Release 由三件套组成：`manifest.json` + `bundle.tar.zst` + `bundle.sig`，位于不可变路径
  `releases/<feature>/<version>/<sha256>/`（`<sha256>` 为 bundle 的 SHA-256）。
- 仓库级 Manifest（`manifests/repository-manifest.json`）对仓库内**每个对象**记录 sha256 + 独立签名；
  节点同步后按它整体校验，任何对象不匹配即失败（`signature_failed`）。

## 2. Feature Manifest（features/<feature_key>/manifest.yaml）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| feature_key | string | 是 | Feature 唯一标识 |
| name | string | 是 | 展示名 |
| version | string | 是 | 语义版本 |
| os | string | 是 | 目标系统（如 linux） |
| arch | string | 是 | 架构（如 amd64 / arm64） |
| source_commit | string | 是 | 源码 commit |
| minimum_agent_version | string | 是 | 最低 Node Agent 版本 |
| dependencies | list | 否 | 依赖 Feature key 列表 |
| backup_mode | string | 是 | `database_dump` / `application_snapshot` / `filesystem_quiesced` / `cold_backup` / `external_snapshot` / `none` |
| rollback_capability | string | 是 | 回滚能力描述（支持回滚到的范围/方式） |
| config_schema | object | 是 | 配置 JSON Schema（见第 7 节） |
| hooks | object | 是 | install/update/backup/health-check/rollback 的固定相对脚本路径 |

`hooks` 结构示例：

```yaml
hooks:
  install: "hooks/install.sh"        # 固定相对脚本路径
  update: "hooks/update.sh"
  backup: "hooks/backup.sh"
  health-check: "hooks/health-check.sh"
  rollback: "hooks/rollback.sh"
```

### 2.1 Hook 脚本约定（强制）

1. **无 MAC 判断**：Hook 不得依赖本机 MAC 地址做身份/授权判断。
2. **不 source 全局 secrets.sh**：Hook 不得 `source` 任何全局 secrets 脚本；Secret 只通过部署根目录内 `0600` 物化文件路径注入。
3. **幂等**：install/update/rollback 必须可重复执行且结果一致；重复执行不产生破坏性副作用。
4. **镜像固定 tag/digest**：Hook 若拉取容器镜像，必须使用固定 tag 或 digest，禁止浮动 `latest`。
5. **health-check 退出码**：仅 `0`（健康）与 `1`（不健康），不得使用其他退出码表达健康状态。
6. **backup 输出单一 tar.zst**：backup Hook 必须把备份内容输出为**单一 `backup.tar.zst`** 到约定输出路径，供主控校验后上传 `backups/.../{backup.tar.zst,metadata.json}`。
7. Hook 必须从 `DEPLOYMENT_ROOT_DIR` 环境/参数获取部署根目录，禁止硬编码绝对路径外的写入。
8. Hook stdout 不得包含 Secret/配置全文；stdout 只保留摘要，不持久化正文（见 13 号文档审计白名单）。

## 3. Release 制品（releases/<feature>/<version>/<sha256>/）

```text
releases/<feature>/<version>/<sha256>/
├── manifest.json
├── bundle.tar.zst
└── bundle.sig
```

### 3.1 manifest.json 字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| feature_key | string | 是 | Feature 标识 |
| version | string | 是 | 语义版本 |
| source_commit | string | 是 | 源码 commit |
| os | string | 是 | 系统 |
| arch | string | 是 | 架构 |
| object_key | string | 是 | 本 Release 对象前缀（必须等于当前路径） |
| size | int | 是 | bundle.tar.zst 字节数 |
| sha256 | string | 是 | bundle.tar.zst SHA-256（小写 hex，与路径 `<sha256>` 一致） |
| signature | string | 是 | bundle 独立签名（bundle.sig 内容） |
| config_schema | object | 是 | 配置 JSON Schema |
| install_hook | string | 是 | 固定相对脚本路径 |
| update_hook | string | 是 | 固定相对脚本路径 |
| backup_hook | string | 是 | 固定相对脚本路径 |
| health_hook | string | 是 | 固定相对脚本路径 |
| rollback_hook | string | 是 | 固定相对脚本路径 |
| dependencies | list | 否 | 依赖 Feature key 列表 |
| minimum_agent_version | string | 是 | 最低 Agent 版本 |
| backup_mode | string | 是 | 备份模式（枚举同上） |
| data_migration_metadata | object | 否 | 数据迁移元数据（迁移脚本、前后版本等） |

### 3.2 不可变性（强制）

- 路径一经发布**不可变**：`releases/<feature>/<version>/<sha256>/` 下的对象不允许覆盖、删除或改写。
- **禁止 `latest` 当真实版本**：不允许以 `latest` 等浮动标签作为版本/路径/校验基准；一切以固定 version + sha256 为准。
- **固定 digest**：bundle 校验使用固定 sha256 + 独立签名；任何变更必须发布新 version 新路径。

## 4. 仓库级 Manifest（manifests/repository-manifest.json）

- 对 `deployment-repository/` 下**每个对象**记录：

```json
{
  "manifest_version": 1,
  "objects": {
    "features/app/manifest.yaml": { "size": 1024, "sha256": "...", "signature": "..." },
    "releases/app/1.0.0/<sha256>/manifest.json": { "size": 2048, "sha256": "...", "signature": "..." },
    "releases/app/1.0.0/<sha256>/bundle.tar.zst": { "size": 1048576, "sha256": "...", "signature": "..." },
    "releases/app/1.0.0/<sha256>/bundle.sig": { "size": 128, "sha256": "...", "signature": "..." }
  },
  "signed_by": "...",
  "created_at": "2026-08-26T00:00:00Z"
}
```

- 节点/主控同步后必须按本 Manifest 逐对象校验 sha256 + 独立签名；任何对象缺失或不匹配即整体失败。
- 本 Manifest 自身也必须是同步与校验的起点（先校验 Manifest 自身的签名）。

## 5. Secret 文件命名（权威）

```text
secrets/shared/<profile>.secrets.yaml          # 共享 Secret Profile
secrets/nodes/<node_id>/<feature>.secrets.yaml # 节点级 Feature Secret
```

- 文件权限 `0600`，所在目录 `0700`。
- **V1 为明文**（YAML 明文，声明见 16 号文档）；加密模式（`aes-gcm` / `kms-envelope`）在对象 key 与数据模型中预留，V1 不强制启用。
- Secret 文件不进入 Git、不入日志/审计/Task 事件/API 响应；数据库只存引用（object_key/version/hash/encryption_mode/metadata）。

## 6. 配置 Schema（强制）

- 每个 Feature 必须在 `manifest.yaml` 与 `manifest.json` 中声明**配置 JSON Schema**（`config_schema`）。
- 缺失 `config_schema` 的 Release 视为 `manifest_invalid`，禁止发布/安装。
- 合并后的最终配置（见 13 号文档第 7 节）在执行前必须通过 JSON Schema 校验；校验失败即操作失败。
- Schema 用于约束 Hook 参数与配置覆盖，禁止将任意对象/任意 key 透传给 Hook。

## 7. 发布流程约束（摘要）

1. 构建产生 `bundle.tar.zst`，计算 sha256 与独立签名，生成 `manifest.json`。
2. 写入不可变路径 `releases/<feature>/<version>/<sha256>/`。
3. 更新仓库级 Manifest 并重新签名。
4. 触发仓库同步（只读凭证）到部署根目录并整体校验。
