# V1 明文敏感配置说明（Plaintext OSS Secrets V1）

> 日期：2026-08-26
> 本文是「部署管理」V1 关于敏感配置（Secret）的权威声明：**V1 明确接受以明文形式存放在私有 OSS**，
> 属于已知安全债务；本文给出 20 条强制保护要求、预留的加密接口与数据库存储边界。
> 实现与评审必须基于此声明展开，禁止在任何输出中复制 init/centos 真实 Secret。

## 1. 范围与声明

- V1 中，Secret 内容以明文 YAML 存放在**私有 OSS** 的 `deployment-repository/secrets/` 前缀下（`secrets/shared/<profile>.secrets.yaml` 与 `secrets/nodes/<node_id>/<feature>.secrets.yaml`）。
- 明文仅存在于：私有 OSS 对象 + 部署根目录内 `0600` 物化文件 + 进程内短时内存。
- 明文**绝不**进入：数据库、Task 事件、日志、审计、事件/通知、API 响应、Git 仓库、Hook stdout、预签名 URL query。
- 加密（`aes-gcm` / `kms-envelope`）在数据模型与接口中预留，V1 不强制启用，V2 目标默认启用。

## 2. V1 明文敏感配置保护要求（20 条，强制）

1. **书面声明**：任何涉及 Secret 的设计/实现必须引用本声明，不得在未评审的情况下改变「V1 明文存私有 OSS」的事实。
2. **私有 Bucket**：Secret 所在 Bucket 必须私有，禁止公共读/公共写/ACL 公开。
3. **最小权限前缀**：仅通过最小权限 RAM 策略访问 `deployment-repository/secrets/` 与 `backups/` 前缀。
4. **禁止主账号 AK / 禁止 AliyunOSSFullAccess**：任何 Secret 访问链路不得使用主账号 AccessKey，不得授予 `AliyunOSSFullAccess`。
5. **首同步只读**：仓库首次同步仅使用只读凭证，写入由精确前缀凭证完成。
6. **凭据不落库明文**：OSS Credential 不落库明文、不入日志/审计/事件/API 响应；数据库仅存引用或加密。
7. **Secret 只存引用**：数据库只存 `object_key` / `version` / `hash` / `encryption_mode` / `metadata`，绝不存正文。
8. **UI 仅覆盖**：管理界面只提供 Secret「覆盖」（overwrite，生成新 version），不提供读取/回显明文入口。
9. **文件权限**：Secret 文件 `0600`、目录 `0700`；`.servercli-local/credentials` 不参与 OSS 同步。
10. **固定命名**：Secret 文件命名遵循固定结构（见 15 号文档第 5 节），禁止任意路径。
11. **key 白名单**：Secret 对象 key 必须命中固定结构白名单（`secrets/shared/<profile>.secrets.yaml`、`secrets/nodes/<node_id>/<feature>.secrets.yaml`），禁止任意 key 上传。
12. **不进 Git**：Secret 文件不进入 Git；`repository/` 部署目录禁止 `git add`。
13. **不写入输出**：备份/还原/Hook 不得将 Secret 明文写入 Hook stdout、日志或审计 reason。
14. **传输加密**：Secret 上传/下载全程 HTTPS（强制 TLS），Endpoint 白名单，禁止明文 HTTP。
15. **轮换走覆盖**：Secret 轮换只能通过 overwrite 接口执行；每次覆盖生成新 version，旧版本按保留策略清理。
16. **节点物化与清理**：节点端仅在部署根目录 secrets 区物化 `0600` 文件，进程用完即调用 Cleanup 清理。
17. **低权限运行**：Node Agent 与 Hook 以低权限账号运行，只能读取本节点所需 Secret。
18. **禁止 source 全局 secrets.sh / argv 传 Secret**：Hook 不得 source 全局 secrets 脚本，不得把 Secret 放入 argv。
19. **审计只记元数据**：审计 reason 只记录 `reason_length`、version、hash、encryption_mode，禁止记录内容。
20. **执行期冻结**：每次 Operation 冻结 secret reference（object_key/version/hash/encryption_mode），执行期不得中途变更；校验失败即失败。

## 3. 预留加密接口（权威）

以下为 Repository Secret Provider 与 Codec 接口契约，V1 实现 `PlaintextSecretCodec`（mode=`none`），
其余 Codec 为 V2 预留，接口签名本期定稿。

### 3.1 RepositorySecretProvider

| 方法 | 签名语义 | 说明 |
| --- | --- | --- |
| ResolveReference | `ResolveReference(ref) → SecretReference` | 按引用解析出 object_key/version/hash/encryption_mode/metadata（不触达正文） |
| ValidateMetadata | `ValidateMetadata(ref, metadata) → error` | 校验元数据一致性（hash、version、encryption_mode、key 白名单） |
| Materialize | `Materialize(ref, destDir) → path` | 将 Secret 物化到部署根目录 secrets 区（0600），返回路径 |
| Cleanup | `Cleanup(path)` | 清理物化文件（进程用完即调用） |

### 3.2 RepositorySecretCodec

| 方法 | 签名语义 | 说明 |
| --- | --- | --- |
| Mode | `Mode() → string` | 返回加密模式标识（`none` / `aes-gcm` / `kms-envelope`） |
| Decode | `Decode(obj) → plaintext` | 对象内容 → 明文 |
| Encode | `Encode(plaintext) → object` | 明文 → 对象内容 |
| Validate | `Validate(obj, metadata) → error` | 校验对象与元数据 |

### 3.3 内置 Codec

| Codec | mode | V1 状态 |
| --- | --- | --- |
| PlaintextSecretCodec | `none` | V1 启用（默认） |
| AESGCMSecretCodec | `aes-gcm` | 预留（V2） |
| KMSEnvelopeSecretCodec | `kms-envelope` | 预留（V2） |

- `deployment_secret_reference.encryption_mode` 默认 `'none'`；当 mode 非 `none` 时，Decode/Encode 必须经对应 Codec，禁止明文回退。
- 数据模型与 key 结构已为加密对象预留字段（`encryption_mode` / `metadata`），V2 升级不改表结构。

## 4. 数据库只存引用（强制）

- `deployment_secret_reference` 与 `deployment_target_secret` 只存：

```text
object_key / version / hash / encryption_mode / metadata
```

- 禁止新增任何正文/明文列；正文只存在于 OSS 对象与节点 `0600` 物化文件。
- `hash` 为内容 SHA-256，用于校验与审计关联；`metadata` 仅含长度、文件名、mime 等非敏感信息。

## 5. 风险声明与已知安全债务

**风险声明**：V1 的 Secret 明文存放于私有 OSS，其安全性完全依赖 OSS Bucket 私有、最小权限 RAM、
HTTPS 传输、key 白名单与节点 0600 物化等控制；任何一条控制被绕过（如 Bucket 被改公共、主账号 AK 泄露、
RAM 策略过宽、日志脱敏失效）都可能导致 Secret 泄露。

**已知安全债务（V2 消除）**：

1. Secret 明文静止存储（应改为 `aes-gcm` / `kms-envelope` 加密对象）。
2. 无服务端 KMS 集成（V2 引入 KMSEnvelopeSecretCodec）。
3. Secret 无自动轮换（V1 仅支持人工 overwrite）。
4. 无按节点细粒度加密密钥（V1 仅靠前缀与权限隔离）。

**验收红线**：任何输出（文档、代码、测试、日志、对话）不得复制 init/centos 真实 Secret；
示例一律使用占位符（如 `LTAI...` 或 `<access-key-id>` 占位）。

## 6. V1 签名与凭证债务

**V1 制品/仓库签名（非公钥体系，属 V1 债务）**：

- V1 中 `deployment-repository/` 制品与 `repository-manifest.json` 的完整性签名使用**控制面生成的 HMAC-SHA256 对称密钥**（`deploy-signing.key`）。
- 密钥仅存在于控制面与节点：节点经 `bootstrap materialize` 一次性下发（base64 传输），写入部署根目录 `.servercli-local/credentials/deploy-signing.key`（root-only `0600`，目录 `0700`），绝不写入 OSS、绝不进入日志/审计/API 响应。
- 该密钥为对称共享密钥，**不是公钥体系**：控制面与所有节点共享同一签名密钥，任何持有该密钥的节点都可伪造/篡改仓库内容，属 V1 已知债务。
- **V2 债务消除方向**：切换为 Ed25519 / RSA 独立签名（控制面持私钥、节点仅持公钥验证），节点无需再持有可签名的对称密钥；接口与 manifest 的 `signature`/`signed_by` 字段已为密钥体系切换预留。

**V1 长期 OSS 凭证（root-only，属 V1 债务）**：

- V1 中节点的后续仓库同步与备份上传使用**长期 OSS AccessKey**（`oss-profile.json`，root-only `0600`，位于 `.servercli-local/credentials/`，不参与 OSS 同步）。
- 该长期凭证已在 UI、审计与本文档中明确标记为 V1 债务；其泄露面受 `*.aliyuncs.com` HTTPS Endpoint 白名单与最小权限 RAM 策略约束。
- **V2 债务消除方向**：替换为 STS 短期临时凭证（`sts-token` 定时续期，最小权限、按需授权），节点不再持有长期 OSS 密钥。
