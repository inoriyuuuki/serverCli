# 部署管理安全设计（Deployment Security）

> 日期：2026-08-26
> 本文是「部署管理」模块的安全权威契约，与 `13_DEPLOYMENT_MANAGEMENT.md`、`15_FEATURE_BUNDLE_CONTRACT.md`、
> `16_PLAINTEXT_OSS_SECRETS_V1.md` 配套。安全要求为强制项，实现不得放宽。

## 1. 威胁模型（概要）

- 制品仓库被篡改：恶意 Feature Bundle / Manifest 被同步并执行。
- Secret 泄露：明文 Secret 进入日志、审计、Task 事件、数据库或 API 响应。
- 越权上传/下载：节点或调用方写任意 OSS key、读非授权前缀。
- SSRF：控制面被诱导访问非白名单 Endpoint。
- 解压逃逸：Bundle 解压写入部署根目录之外（`../`、绝对路径、软硬链接、设备文件）。
- 提权：Hook 或 Agent 以过高权限执行，或经 sudo wrapper 逃逸。
- 资源滥用：超大 Bundle、海量文件、磁盘耗尽。

## 2. OSS 安全基线（强制）

1. **Bucket 私有**：Bucket 必须为私有读/写，禁止公共读、公共写、ACL 公开、静态网站公开。
2. **RAM 最小权限**：仅授予 `deployment-repository/` 与 `backups/` 两个前缀的最小权限：
   - 同步凭证：只读 `deployment-repository/`；
   - 发布凭证：写 `deployment-repository/`（精确前缀，供主控发布用）；
   - 备份凭证：仅写 `backups/`（精确前缀，短时效）。
3. **首同步只读凭证**：首次仓库同步只使用只读凭证，禁止使用可写凭证做初始化。
4. **备份上传精确前缀**：节点上传备份必须使用服务端生成的精确前缀授权（`backups/<environment>/<feature>/<node_id>/<yyyy>/<mm>/<dd>/<operation_id>/`），节点不可自行指定任意 key。
5. **禁止主账号 AK**：任何代码、脚本、配置、文档不得使用阿里云主账号 AccessKey。
6. **禁止 `AliyunOSSFullAccess`**：任何 RAM 策略不得授予该权限或等价通配权限；违反视为 P0。
7. OSS Credential 不落库明文、不入日志/审计/事件/API 响应；数据库仅存引用或加密（`credential_ref`）。

## 3. SSRF 防护（强制）

- **Endpoint allowlist**：控制面/节点只允许访问配置白名单内的 OSS Endpoint（域名精确匹配，拒绝 IP 直连、内网网段、云元数据地址 `169.254.169.254`、`localhost`/`127.0.0.1`）。
- **HTTPS 强制**：`use_https=true`，禁止明文 HTTP；TLS 证书校验开启。
- 不允许用户提供任意 URL 作为同步/上传目标；目标必须由 `oss_profile` 白名单推导。
- DNS 解析结果需二次校验命中 allowlist；重定向目标同样受校验。

## 4. Object Key 白名单（强制）

- 对象 key 必须匹配固定结构，禁止任意 key：
  - 仓库：`deployment-repository/{catalog,features,releases,configs/shared,configs/nodes,secrets/shared,secrets/nodes,manifests}/...`
  - Release 制品：`releases/<feature>/<version>/<sha256>/{manifest.json,bundle.tar.zst,bundle.sig}`
  - 备份：`backups/<environment>/<feature>/<node_id>/<yyyy>/<mm>/<dd>/<operation_id>/{backup.tar.zst,metadata.json}`
- `<feature>`、`<version>`、`<node_id>`、`<operation_id>` 等段必须匹配安全字符集（`[a-zA-Z0-9._-]` 且禁止 `..`），服务端生成，禁止拼接用户原始输入。
- 任何不匹配白名单的 key 一律拒绝（读、写、删、上传授权均拒绝）。

## 5. Secret 处理（强制，详见 16 号文档）

- **Secret 不落库**：数据库只存 `object_key`/`version`/`hash`/`encryption_mode`/`metadata`。
- **不入 Task**：Task 事件/请求体/结果不得携带 Secret 正文。
- **不入事件**：操作事件、通知、Webhook 不含 Secret。
- **不入日志**：日志只允许引用与 hash，禁止正文。
- **不入审计**：见 13 号文档审计白名单，禁止正文/配置全文/OSS Credential/预签名 URL/URL query/Hook stdout/Provider 响应正文。
- **GET 不回显**：任何 API 不返回 Secret 正文；`GET /deployments/secrets/references` 只返回引用元数据。
- **UI 只允许覆盖**：界面只提供 `overwrite`（生成新 version），不提供读取/查看明文入口。

## 6. P0 整改项（强制，本期必须落地）

以下为与部署管理相关的存量/新增 P0 整改，实现与验收必须逐项落实：

1. **init 泄露 Secret 轮换**：对 init/一键脚本历史泄露的 Secret 进行轮换；新脚本不再内嵌任何真实 Secret。
2. **禁 argv 传 AK**：禁止通过命令行参数（argv）传递 AccessKey/Secret；一律经环境引用、`0600` 文件或 Secret 管理服务注入。
3. **禁 `curl | sh` 长脚本**：禁止从不可信地址下载脚本后直接管道执行长脚本；脚本必须固定版本、固定 hash 校验后再执行。
4. **收紧 `servercli-ai` sudoers**：收紧 AI 租户账号 sudo 规则，只允许精确 wrapper 命令，禁止通配符提权。
5. **Redactor 覆盖阿里云 AK**：日志 Redactor 必须覆盖阿里云 AccessKey ID（`LTAI...` 模式）与 AccessKey Secret，任何日志/审计/事件输出前统一脱敏。
6. **脱敏 task event**：Task 事件（请求、结果、重试、通知）统一脱敏，禁止携带 Secret、配置全文、Hook stdout。
7. **reason 前后端契约**：失败/取消原因统一走「长度 + 错误码」契约（`reason_length`），前后端一致，禁止把完整输出写进 reason。
8. **统一安全解压**：所有 Bundle 解压必须经过统一安全解压实现（见第 9 节检查项），禁止各处手写 `tar` 解压绕过检查。
9. **rm -rf 防护**：任何清理/回滚脚本中的删除操作必须限定在部署根目录内的具体路径，禁止未校验的 `rm -rf`（尤其禁止 `rm -rf /*`、变量为空时的 `rm -rf $DIR`）。
10. **部署目录禁 Git add**：`repository/` 与 `.servercli-local/` 禁止 `git add`/入库；CI 与脚本增加显式排除与检查。

## 7. 制品校验（强制）

1. 每个 Release 制品必须同时具备 **SHA-256**（`bundle.tar.zst` 与 `manifest.json` 内声明一致）与**独立签名**（`bundle.sig`，签名私钥与发布流程隔离）。
2. 仓库级 Manifest（`manifests/repository-manifest.json`）逐对象记录 sha256 + 签名；节点同步后必须按 Manifest 校验，任何对象不匹配即整体失败（`signature_failed`）。
3. 校验在**执行前**完成（`download` → `verify` → `unpack` 步骤顺序固定）；校验失败禁止进入 install/update。
4. 签名验证密钥来自独立信任根，不得与同步凭证同源。
5. **Node Agent 首装制品的获取与校验**：Agent 制品由 GitHub Actions 构建后上传到
   公开读 OSS 桶 `inori-image`（对象名固定 `servercli/latest/servercli-latest-linux-<arch>.tar.gz`
   + `sha256sums.txt`，不区分版本号、始终指向最新构建），节点在首次引导时经
   HTTPS 从该桶下载，并按同桶 `sha256sums.txt` 做 SHA-256 校验后安装
   （与 GitHub Release 流程的校验强度一致；
   Agent 制品的独立签名随 CI 签名体系一并纳入，V1 与 Release 流程对齐，见
   doc/16「V1 签名与凭证债务」）。**该流程不经过 xray，不访问 GitHub**。

## 7.5 Restore（从备份恢复，安全约束）

- 仅允许从 **deployment_backup 记录**恢复（object key 由控制面下发，不接受任意 key）；
  备份必须 status=succeeded、backup_mode != none、且属于目标 feature+node。
- **数据守卫**：目标数据目录已存在且非空时，restore 默认失败（提示先删除原数据）；
  仅当显式 `force_delete=true` 时才允许先迁移/删除原数据再恢复（正式环境需 reason + 二次确认）。
- 备份下载后必须校验 sha256/size，经统一安全解压到 staging 后再写数据目录；恢复后执行
  本地健康检查（hook）+ 控制面二次探活。

## 8. 解压安全检查（强制）

统一安全解压实现必须在解压 `bundle.tar.zst` 时逐项检查：

- `../` 路径穿越：任何成员路径含 `..` 段即拒绝。
- 绝对路径：任何成员路径以 `/` 开头即拒绝。
- 软/硬链接逃逸：链接目标解析后必须仍位于目标前缀内，否则拒绝。
- 设备文件：FIFO/char/block 设备文件拒绝。
- setuid/setgid：拒绝保留 setuid/setgid 位。
- 文件数上限：超过阈值（默认 10000 个成员）拒绝。
- 总大小上限：解压后总大小超过阈值（默认 2 GiB，可配）拒绝。
- 磁盘剩余：解压前检查目标分区剩余空间（至少 2 倍预估），不足拒绝。
- 目标前缀：解压仅允许写入 `staging/` 下固定前缀，之后原子移动/渲染到目标区。

## 9. 固定 deployment.* 命令（强制）

- 节点端只执行**固定命名**的 `deployment.*` 命令（如 `deployment.install`、`deployment.update`、`deployment.backup`、`deployment.health-check`、`deployment.rollback`、`deployment.cleanup`），由主控按 Feature Manifest 中声明的固定相对脚本路径映射。
- 禁止执行任意路径/任意参数拼接的 Shell；命令参数必须经过 Schema 校验并以 argv 传递。
- Hook 脚本来自已校验 Bundle，镜像使用固定 tag/digest，脚本无 MAC 判断、不 source 全局 secrets.sh（见 15 号文档）。

## 10. Agent 低权限 + 精确 sudo wrapper（强制）

- Node Agent 以低权限专用账号运行（非 root）。
- 需要提权的操作（服务启停、安装系统文件等）仅通过**精确 sudo wrapper**：wrapper 只允许白名单内的固定命令与固定参数模板，拒绝通配符与用户输入直通。
- sudoers 条目逐条精确列出 wrapper 路径与参数，禁止 `ALL`、禁止 `sudo su`、禁止无参通配。
- Hook Runner 继承 Agent 的低权限，额外权限由 wrapper 提供并在进程结束时清理。

## 11. 取消杀进程组（强制）

- 操作取消/超时/失败时，必须向目标进程组发送终止信号（先 `SIGTERM`，宽限后 `SIGKILL`），确保 Hook 及子进程整体退出，不允许留下孤儿进程继续写入。
- 使用独立进程组运行 Hook（`setpgid`/`setsid`），记录进程组 ID 供取消使用；取消后验证进程确实退出并记录结果。

## 12. 与审计/日志联动

- 审计事件与日志经统一 Redactor 管线输出，白名单见 13 号文档第 11 节。
- 安全事件（命中禁止项、解压检查失败、签名失败、key 白名单拒绝、sudoers 逃逸尝试）必须记录为独立安全事件并触发告警。
