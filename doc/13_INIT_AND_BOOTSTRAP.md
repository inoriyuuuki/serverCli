# 13. 初始化与引导（servercli init / bootstrap）设计文档

> 基线：2026-08-10
> 范围：全新 CentOS/RHEL（EL8/EL9）物理主机上的 ServerCLI 初始化引导子系统。
> 涉及实现：`backend/internal/bootstrap`、`initstate`、`secretstore`、`modman`、
> `sigverify`、`bundle`、`ownership`、`ops`、`deploy/install-servercli.sh`、
> `.github/workflows/build-binaries.yml`、`release/sample-*.json`、`scripts/scan-secrets.sh`。

## 1. 目的与范围

ServerCLI 面向「一台固定主服务器 + 多台节点服务器」的物理主机部署，不依赖 Docker。
`servercli` CLI 是全新主机上运行的第一个程序：在 PostgreSQL、Docker、Gitea、
Control Plane 都不存在时，驱动安装器落盘 → Bundle 导入 → 模块按序预置 →
Secret 入加密 Store → 状态机推进 → 形成可用的 Control Plane + Node Agent。

本子系统满足：

1. **数据库无关**：init 全程不依赖数据库，先建好基础设施模块，最后再启动业务服务。
2. **fail-closed 信任链**：所有下载产物以签名 Release Manifest 为信任锚，绝不信任裸 SHA256。
3. **可恢复**：初始化分阶段记录 commit points，中断后可 resume / repair，不重复副作用。
4. **可审计**：所有目录/文件权限最小化、原子写 + fsync、拒绝符号链接攻击、全程无明文 Secret。
5. **与旧运维兼容**：`/home/init/centos/update.sh` 等旧入口在兼容期继续可用，实际执行统一转 `servercli ops`。

## 2. 总体架构

```mermaid
flowchart TD
    subgraph 发布侧
        CI[GitHub Actions build-binaries.yml]
        CI --> BND[bundle tar.gz + release-manifest.json]
        CI --> SIGN{配置签名私钥?}
        SIGN -->|是| SIG[openssl Ed25519 签名]
        SIGN -->|否| UNS[产物标记 unsigned]
        SIG --> REL[GitHub Release 主源 / OSS 回退源]
        UNS --> REL
    end

    subgraph 安装侧 EL8/9 全新主机
        INS[deploy/install-servercli.sh 仅 root]
        INS --> MAN[下载 release-manifest.json 先 GitHub 后 OSS]
        MAN --> VRF[openssl pkeyutl 验签 + 逐 artifact sha256 校验]
        VRF -->|失败 exit 4| ABORT[终止安装 fail-closed]
        VRF -->|通过| INST[/opt/servercli/releases/vX 原子切换 current/previous]
        INST --> PRM{TTY 且非 --no-init-prompt?}
        PRM -->|是| INIT[servercli init]
        PRM -->|否| DONE[仅安装]
        INIT --> ST[initstate 状态机 state.json]
        ST --> MM[modman 依赖图按序执行模块]
        MM --> CP[commit points 持久化]
        CP --> READY[core_ready / ready]
    end

    subgraph 安全
        SCAN[scripts/scan-secrets.sh 提交前扫描]
        AGE[age 加密 Bundle + Bootstrap Store]
        OWN[owner/adopt 归属控制]
    end
```

## 3. 固定目录布局

所有固定路径由 `backend/internal/bootstrap` 统一定义，任何组件不得自行发明路径。

| 路径 | 用途 | 权限 |
| --- | --- | --- |
| `/etc/servercli` | 节点级配置根 | 0700 root |
| `/etc/servercli/private` | 私有 Inventory（`cluster.yaml`，仅 SecretRef，不内联值） | 0700 root |
| `/etc/servercli/private/services.d` | 服务级私有配置 | 0700 root |
| `/etc/servercli/keys` | 密钥目录（master.key / bootstrap.agekey） | 0700 root |
| `/etc/servercli/keys/master.key` | 首次 Bundle 导入生成的加密主密钥 | 0600 root |
| `/etc/servercli/keys/bootstrap.agekey` | age X25519 身份（Bundle 解密用） | 0600 root |
| `/var/lib/servercli` | 状态数据根 | 0700 root |
| `/var/lib/servercli/bootstrap` | init 状态与 Bootstrap Store | 0700 root |
| `/var/lib/servercli/bootstrap/state.json` | 初始化状态机持久化 | 0600 root |
| `/var/lib/servercli/bootstrap/secrets.enc` | 本地加密 Bootstrap Store | 0600 root |
| `/var/lib/servercli/postgres` | Foundation PostgreSQL 数据目录 | 0700 root |
| `/var/lib/servercli/state` | 节点运行状态 | 0700 root |
| `/var/lib/servercli/backups` | 备份/恢复集 | 0700 root |
| `/run/servercli` | 运行时临时区（tmpfs） | 0700 root |
| `/run/servercli/bootstrap` | Bundle 明文短暂驻留区，完成后清理 | 0700 root |
| `/run/servercli/operations` | 操作期 Secret 临时文件（0600） | 0700 root |
| `/opt/servercli` | 安装根（由安装器维护） | 0755 root |
| `/opt/servercli/releases/<version>` | 每个版本的完整 Release 内容 | 0755 root |
| `/opt/servercli/current` | 指向当前版本（软链接，staging + rename 原子切换） | 软链接 |
| `/opt/servercli/previous` | 指向上一个版本（软链接） | 软链接 |

约定：

- 新代码一律 root-only：目录 0700、文件 0600/0644，二进制 0755。
- 拒绝符号链接：关键文件路径在打开前 `lstat` 校验，禁止跟随链接写入。
- 所有持久化写入采用「临时文件 + fsync + rename」原子替换。

## 4. 三二进制与 CLI 公共接口

| 二进制 | 职责 | 入口 |
| --- | --- | --- |
| `servercli` | 初始化/运维 CLI（数据库无关） | `backend/cmd/servercli` |
| `servercli-control-plane` | 主控服务（REST API + 前端 + 调度） | `backend/cmd/control-plane` |
| `servercli-node-agent` | 节点 Agent（主动外连主控） | `backend/cmd/node-agent` |

`servercli` 稳定命令接口：

```text
servercli init | init plan | init apply | init status | init resume | init repair
servercli config import plan | servercli config import apply
servercli modules run --module <id> --operation <op> [--yes] [--output json]
servercli ops update|backup|restore [service...] [--output json]
servercli version
```

公共参数：`--environment`、`--node-name`、`--bundle-url`、`--age-key-file`、
`--yes`、`--output=json`。**禁止通过 argv 传递 Secret**：Secret 只经
`/run/servercli/operations` 下的 0600 临时文件或单行环境变量注入。

稳定退出码（`backend/internal/bootstrap`，不可重编号）：

| 码 | 名称 | 含义 |
| ---: | --- | --- |
| 0 | ok | 成功 |
| 2 | usage_error | 参数/用法错误 |
| 3 | preflight_failed | OS/arch/DNS/端口/依赖等预检失败 |
| 4 | signature_failed | 签名/认证失败（Release/Bundle 验签、age 解密、claim 鉴权） |
| 5 | network_failed | 临时网络失败（可重试） |
| 6 | module_failed | 模块执行失败 |
| 7 | partial_success | 部分成功 |
| 8 | blocked | 并发 init / owner 冲突 / 无 ownership 元数据 / 需人工决策 |
| 9 | manual_action_required | 需人工处理（凭据轮换、DNS、防火墙等） |

## 5. 发布信任链（Release Manifest + 安装器）

### 5.1 信任模型

- **Release Manifest**（`release-manifest.json`）由同一 Ed25519 发布私钥签名；
  GitHub Release 为主源、OSS 镜像为回退源，两源产物必须用同一公钥验证。
- Manifest 内 `artifacts[].sha256` 摘要表是唯一信任锚；**禁止只信裸 SHA256**。
- 发布私钥绝不进入仓库/CI 日志；CI 通过 Secret `SERVERCLI_RELEASE_SIGNING_KEY`
  注入 base64 编码的 Ed25519 私钥 PEM。
- 发布公钥由运维线下分发（`--pubkey <file>`）；安装器内嵌的占位公钥不是真实密钥，
  使用它验签必然失败（fail-closed）。

### 5.2 签名消息（canonical form）——三端必须一致

签名消息 = **去除 `signature` 字段后，按键名排序的紧凑 JSON**。三端等价实现：

- CI（Python）：`json.dumps(m, sort_keys=True, separators=(",", ":"), ensure_ascii=False)`
- 安装器（jq）：`jq -cS 'del(.signature)'`
- Go 验签端：解码为 `map[string]interface{}` 后 `json.Marshal`（encoding/json 对
  map 按键名排序，与上述输出字节一致；已做字节级互操作验证）

签名流程（CI 签名步骤）：

1. 生成 canonical 消息（jq/Python 与 Go 字节一致）。
2. `openssl pkeyutl -sign -rawin -inkey <私钥PEM> -in <canonical>` 直接对 canonical
   原文做标准 Ed25519 签名（无 SHA-256 预哈希）。
3. 签名 base64 后写入 manifest 的 `signature` 字段。

验签流程（安装器）：

1. 重建 canonical 消息（jq 或 python3，与 CI/Go 相同实现）。
2. 解 base64 得到签名。
3. `openssl pkeyutl -verify -rawin -pubin -inkey <公钥> -in <canonical> -sigfile <签名>`
   —— 与 Go `ed25519.Verify(pub, canonical, sig)` 语义一致（均验 raw canonical 原文）。
4. 失败即 `exit 4`，不继续安装。

Go 端 `bundle.CanonicalManifestBytes` 与 `sigverify.VerifyEd25519` 遵循同一约定，
并有 `canonical_test.go` 锁定与 `jq -cS 'del(.signature)'` 字节一致的固定向量。

### 5.3 安装器流程（deploy/install-servercli.sh）

1. 预检：仅 root；`/etc/os-release` 识别 CentOS/RHEL/EL8/EL9；`uname -m` 支持
   `x86_64` / `aarch64`；要求 openssl（缺失时提示先 `dnf install openssl`）、
   curl/wget、jq 或 python3。
2. 参数：`--version`（默认 `releases/latest`）、`--github-base`（默认
   `https://github.com/inoriyuuuki/serverCli/releases/download`）、`--oss-base`（可空）、
   `--pubkey`（缺省用内嵌占位公钥并打印必须替换警告）、`--yes`、`--no-init-prompt`。
3. 下载 `release-manifest.json`：GitHub 主源 → OSS 回退；失败 `exit 5`。
4. 验签（见 5.2）；失败 `exit 4`。
5. 按 `artifacts[]` 逐项下载并 `sha256sum -c` 校验；目录型 artifact
   （`modules/`、`templates/`、`schema/`）以 `<name>.tar.gz` 发布，解压时
   `--strip-components=1` 落到对应子目录；二进制/安装器 `install -D -m 0755`。
6. 安装到 `/opt/servercli/releases/<version>`（版本取自 manifest 的
   `release_version`，并做字符白名单校验）；同目录已存在则先清除再装。
7. 原子切换：`previous` 记录旧 `current`；`ln -sfn releases/<v> .current-staging`
   再 `mv -Tf .current-staging current`（rename 原子替换）。
8. init 询问：仅当 stdin 为 TTY 且未指定 `--no-init-prompt` 时询问
   「是否运行 servercli init」；`--yes` 直接运行；非 TTY 或 `--no-init-prompt`
   只安装不等待。运行 init 时其退出码原样透传。

下载 URL 形态：

- GitHub 最新版：`https://github.com/inoriyuuuki/serverCli/releases/latest/download/<asset>`
- GitHub 指定 tag：`https://github.com/inoriyuuuki/serverCli/releases/download/<tag>/<asset>`
- OSS 镜像：`<oss-base>/<version>/<asset>`（`latest` 目录对应最新版）

> 多架构发布约定：一个 Release 对应一个平台（默认 linux/amd64）；arm64 使用
> 对应平台的 tag 或 OSS 目录。首版完整 Foundation 模块只保证 amd64。

## 6. Bundle 与 age

- **Bundle Manifest** 字段（`backend/internal/bootstrap.BundleManifest`）：
  `schema_version`、`bundle_id`、`bundle_version`、`environment`、`target_node`、
  `target_role`、`created_at`、`minimum_bootstrap_version`、`payload_digest`、
  可选 `expires_at`、`signature`/`signing_key_id`。
- Bundle 载荷为 SOPS/age 加密（X25519），由 `sigverify.DecryptAge` 解密；
  身份密钥默认 `/etc/servercli/keys/bootstrap.agekey`（0600）。
- 明文 Bundle 只允许短暂存在于内存或 `/run/servercli/bootstrap`（tmpfs），
  处理完成后立即清理。
- **重放保护**：生产环境默认拒绝低于当前版本/过期/重复 `bundle_id` 的 Bundle；
  URL 内容变化不得被 `resume`/`repair` 自动接受，必须走 `config import plan/apply`
  人工确认。
- **首次导入**：
  1. 生成 `master.key`（0700/0600、root、拒符号链接/错误 owner/宽松权限）。
  2. 初始 Secret 写入本地加密 Bootstrap Store（`/var/lib/servercli/bootstrap/secrets.enc`）。
  3. PostgreSQL + Control Plane ready 后事务性导入正式 Secret Store；
     交接后 Bootstrap Store 转只读并清理，**不允许存在两份可写权威源**。
- 随机生成的 Secret 必须先原子写入 Store 并 fsync，再用于外部资源创建；
  重试必须复用同一版本（幂等）。

## 7. 初始化状态机（initstate）

持久化：`/var/lib/servercli/bootstrap/state.json`（文件锁 + 校验和 + 原子 rename）。

整体状态：

```text
not_initialized -> initializing -> core_ready -> ready
                        |  \---------> degraded
                        \-----------> failed / blocked
```

- `not_initialized`：未初始化，`init plan` 可生成计划。
- `initializing`：进行中，`init status` 可查看步骤；`resume`/`repair` 从
  最后一个 commit point 继续。
- `core_ready`：foundation-core 全部就绪（v2ray/docker/postgres/caddy/control-plane/agent）。
- `ready`：foundation 完整（含 gitea）且健康检查通过。
- `degraded`：部分模块失败但已保留可用子集。
- `failed` / `blocked`：不可自动恢复 / 需要人工或归属决策。

步骤状态：`pending` / `running` / `succeeded` / `skipped` / `failed` / `blocked`。
错误分类：`preflight` / `signature` / `network` / `module` / `blocked` / `manual` / `unknown`。

保障：

- 并发 init 通过独占文件锁拒绝（`ErrConcurrent`）。
- 状态文件损坏 → 只读诊断，绝不自动重新初始化（`ErrCorrupt`）。
- 状态迁移白名单由 `CanTransition` 校验，非法迁移直接拒绝。
- commit points 持久化后才允许下一步，中断恢复不重复副作用。

## 8. 模块清单与执行顺序（modman）

模块清单：仓库根 `modules/<id>/module.yaml` + `operations/*.sh`。
`module.yaml` 结构：`id` / `version` / `phase` / `depends_on` / `config_fields` /
`secret_fields` / `delivery` / `operations` / `healthcheck` / `backup` / `concurrency`。

阶段与顺序（foundation-core 严格按序）：

| 顺序 | 模块 | 阶段 | 关键约束 |
| ---: | --- | --- | --- |
| 1 | v2ray | foundation-core | enabled 由 Inventory 控制；disabled 时须先验证 GitHub/OSS/镜像仓库直连；不覆盖无 ownership 的既有代理配置；为 curl/git/docker daemon/模块下载器配置代理 |
| 2 | docker | foundation-core | 固定支持版本，已有兼容安装可复用；不清理非 ServerCLI 容器/镜像/volume；受管资源打 managed-by/module-id/instance-id/配置摘要标签 |
| 3 | postgres | foundation-core | `paradedb/paradedb:pg17` 固定摘要；数据目录 `/var/lib/servercli/postgres`；独立库 + 最小权限账号；`pg_isready` + 实际连接通过才继续；重复执行不重建/不重置；已有非空目录且无 ownership 元数据 → blocked；默认不自动恢复 |
| 4 | caddy | foundation-core | Docker bridge + host-gateway（禁止硬编码网桥 IP）；两阶段（维护模式先取 ACME TLS → 正式路由切换）；维护页不暴露内部状态；ACME 失败 → degraded 且保留 postgres/docker/v2ray |
| 5 | control-plane | foundation-core | postgres + caddy gateway ready 后启动；生产强制 PostgreSQL；初始经专用网桥地址提供 Caddy；健康接口全过后记录 `control_plane_local_ready` |
| 6 | agent | foundation-core | root-only Unix Socket / 本地 Bootstrap 通道 claim（Token 不落 argv/env/日志；绑定 environment/node-name/primary role/init transaction ID/Agent Ed25519 公钥；短期单次原子消费）；claim 后切 Caddy HTTPS；心跳成功记录 `agent_ready`/`core_ready` |
| 7 | gitea | foundation-services | 属 foundation 但不属于 core_ready 硬门禁；新装走 Foundation PostgreSQL 独立库；旧实例 adopt 保持 MariaDB 10.11，adopt/repair/update 不得隐式迁移 |

commit points：

```text
postgres_ready -> caddy_gateway_ready -> control_plane_local_ready
  -> caddy_route_ready -> agent_claimed -> agent_ready -> core_ready
foundation_complete = core_ready + gitea
```

操作白名单（`modman.AllowedOperations`）：`install` / `uninstall` / `verify` /
`backup` / `restore` / `adopt` / `preflight` / `plan`。Runner 只执行
`<module>/operations/<op>`，绝不执行任意路径或命令；`delivery` 支持
`env`（单行值）与 `file`（0600 临时文件，经 `/run/servercli/operations`）。

## 9. owner / adopt（ownership）

每项 `environment + node + service` 有唯一 owner：

```text
legacy-init -> migration-frozen -> adopting -> servercli -> rollback-pending
```

- ServerCLI **仅在 owner=servercli 时**执行 install / update / backup / restore。
- adopt 固定流程：只读发现 → 差异计划 → 冻结旧 cron/timer/install/update/backup/restore
  → 等旧任务结束 → 创建迁移备份 → 校验目录/容器/端口/数据库/版本 →
  写 ownership metadata → 导入配置与 SecretRef → 健康检查 → 切换 owner →
  禁用旧入口。
- adopt 失败：不得移动/删除/重建原数据，恢复 legacy owner。
- **MAC 地址只能用于迁移信息/异常提示**，绝不用于身份、角色、授权、自动批准或
  Secret 授权（`Inventory.LegacyMAC` 仅为迁移元数据）。

## 10. ops update/backup/restore 兼容

- 兼容入口（兼容期保留，模板在 `deploy/compat/`）：
  `/home/init/centos/update.sh`、`/home/init/centos/backup.sh`、
  `/opt/servercli/update.sh`、`/opt/servercli/backup.sh`。
  兼容语义：无参 = 全部服务；指定服务名 = 指定；单项失败继续；聚合退出码；
  不依赖 cwd；透传 stdin/stdout/stderr/信号/退出码。
- 实际执行统一转 `servercli ops update|backup|restore`；节点级 + 服务级锁，
  同一服务不能同时 update/backup/restore/adopt。
- 备份生成 `backup_id` / `recovery_set_id` / 版本 / schema 版本 / 时间点 /
  文件摘要 / 依赖顺序 / 签名 manifest；完成条件含远端上传、摘要验证、远端回读
  （远端上传留 adapter 接口）。
- 恢复必须显式指定 `backup_id` 或 `recovery_set_id`；普通安装/空目录不自动恢复 latest。
- 旧备份经只读 legacy catalog/import adapter 识别，明确标注缺失元数据与校验能力。
- **数据库不可逆迁移**：Release Manifest 声明 schema 兼容区间与可逆性
  （`SchemaCompat.min/max/reversible`）；更新前维护模式/写冻结；生成并验证 DB 备份；
  不可逆迁移后禁止自动回滚旧二进制；恢复 DB 是显式高风险操作。

## 11. 安全门禁

1. **root-only + 最小权限**：安装器仅 root；目录 0700、敏感文件 0600。
2. **fail-closed 验签**：Manifest 签名或任一 artifact sha256 不匹配即终止（exit 4）；
   无 `signature` 字段的 unsigned 产物直接拒绝。
3. **无明文 Secret**：Secret 只进加密 Store / 0600 文件 / 单行 env；
   Bundle 明文只短暂存在于 `/run/servercli/bootstrap` tmpfs。
4. **原子写 + fsync + 文件锁**：state/secrets/master.key 全部原子替换。
5. **拒绝符号链接**：关键路径打开前校验，禁止链接写入。
6. **提交前扫描**：`scripts/scan-secrets.sh` 检测私钥块、AWS/OSS AccessKey、
   通用 Token、认证 URL、宽松权限、source 引入 secrets、明文密码赋值；
   命中即 exit 1。
7. **CI 无密钥泄露**：签名私钥仅经 Secret 注入，日志只输出摘要与警告。
8. **重放/降级保护**：生产拒绝低版本 Bundle 重放；URL 内容变化须人工确认。
9. **两份权威源禁止**：Bootstrap Store 与正式 Secret Store 交接后只保留一份可写源。
10. **旧入口兼容期内审计**：所有 ops 调用统一走 `servercli ops`，保留审计。

## 12. 关键验收场景 → 实现映射

| 验收场景 | 要求 | 实现位置 | 验证方式 |
| --- | --- | --- | --- |
| 全新 EL9 x86_64 主机一键安装 | root 检测、EL8/9 + x86_64/aarch64、GitHub→OSS 回退、验签、sha256、原子 current/previous | `deploy/install-servercli.sh` | 脚本离线审查 + bash -n；EL 主机手工 |
| 下载产物被篡改 | 验签/摘要失败即 exit 4，不落盘 | 安装器 5.2/5.3 | 篡改 manifest/artifact 后运行 |
| CI 无签名私钥 | 生成 unsigned 标记 manifest，跳过签名并告警 | `build-binaries.yml` Sign step | 不设置 Secret 运行 workflow |
| CI 有签名私钥 | openssl Ed25519 签名写入 manifest，含 sha256 摘要表 | `build-binaries.yml` Sign step | 设置 Secret 运行 workflow，安装器可验签 |
| 三个二进制发布 | servercli / control-plane / node-agent 同 -ldflags 构建 | `build-binaries.yml` binaries job | workflow 运行后检查 bin/ |
| bundle 内容完整 | modules/、deploy/install-servercli.sh、release/ 打进 bundle 且安装器可执行 | `build-binaries.yml` Package step | 解包检查 + `test -x` |
| 模块按序执行 | foundation-core 严格顺序与依赖图 | `backend/internal/modman` + `modules/` | `go test ./...` + 状态机回放 |
| 初始化中断后续跑 | commit points + resume/repair，不重复副作用 | `backend/internal/initstate` | 中断点故障注入测试 |
| 并发 init | 独占锁拒绝 | initstate `ErrConcurrent` | 双进程并发测试 |
| Secret 不落盘明文 | age 加密 Bootstrap Store + master.key 权限 | `backend/internal/secretstore` + `sigverify` | 文件内容检查 + 权限断言 |
| 仓库泄密检测 | 私钥块/AWS/OSS/Token/认证 URL/宽松权限（权限位 777）/加载 secrets 文件的 source 调用/明文密码赋值 | `scripts/scan-secrets.sh` | 命中 exit 1；无命中 OK |
| 旧入口兼容 | update.sh/backup.sh 无参=全部、指定=指定、聚合退出码 | `deploy/compat/` + `backend/internal/ops` | 兼容测试用例 |
| 低版本 Bundle 重放 | 生产拒绝低版本/过期/重复 bundle_id | `backend/internal/bundle` | replay 测试 |
| 不可逆迁移 | SchemaCompat 声明 + 禁止自动回滚旧二进制 | `backend/internal/bootstrap` + ops | schema 迁移测试 |
| 发布示例不泄密 | sample manifest 全占位、无真实密钥 | `release/sample-*.json` | 扫描 + 人工审查 |

## 13. 相关文件索引

| 文件 | 说明 |
| --- | --- |
| `backend/internal/bootstrap/types.go` | 稳定契约：退出码、Release/Bundle Manifest、固定目录 |
| `backend/internal/initstate/` | 状态机、步骤、commit points、resume/repair |
| `backend/internal/secretstore/` | MasterKey、加密 Store、.env 解析 |
| `backend/internal/modman/` | 模块 manifest、依赖图、受限 Runner |
| `backend/internal/sigverify/` | Ed25519 验签/签名、age 解密 |
| `backend/internal/bundle/` | Bundle 导入与重放保护 |
| `backend/internal/ownership/` | owner 状态机与 adopt 流程 |
| `backend/internal/ops/` | update/backup/restore 与锁 |
| `modules/` | 公共模块（v2ray/docker/postgres/caddy/control-plane/agent/gitea） |
| `deploy/install-servercli.sh` | 安装器（本子系统发布入口） |
| `deploy/INSTALL.md` | 安装说明（含 servercli CLI 安装器章节） |
| `release/sample-release-manifest.json` | 示例 Release Manifest（占位） |
| `release/sample-bundle-manifest.json` | 示例 Bundle Manifest（占位） |
| `.github/workflows/build-binaries.yml` | 三二进制构建 + bundle 打包 + 签名步骤 |
| `scripts/scan-secrets.sh` | 提交前泄密扫描 |
| `backend/cmd/servercli` | servercli CLI 入口 |
