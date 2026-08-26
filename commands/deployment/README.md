# deployment.* 固定命令（部署管理）

本目录声明「部署管理」模块的 6 个固定命令 manifest。它们**不经过通用 exec 执行**：
`spec.executable` 固定为 `./deployment/deployment-runner`（占位，仅用于 manifest 注册与存在性校验），
实际执行由 Node Agent 内置的 `backend/internal/agent/deployment_runner.go`（DeploymentRunner）处理，
Executor 按 `command_id` 前缀 `deployment.` 分流。

## 命令清单

| command_id             | release_id | 说明                                         |
| ---------------------- | ---------- | -------------------------------------------- |
| `deployment.sync`      | 不需要     | 从私有 OSS 同步 `deployment-repository/` 并整体校验、修正权限 |
| `deployment.install`   | 必填       | 定位/校验 Release，解压到 staging，原子切换渲染目录，执行 install Hook |
| `deployment.update`    | 必填       | 同 install，执行 update Hook                 |
| `deployment.backup`    | 可选       | 执行 backup Hook，上传备份包到 `backups/` 前缀并校验 |
| `deployment.health-check` | 可选    | 执行 health-check Hook，退出码 0=健康 / 非 0=不健康 |
| `deployment.rollback`  | 必填       | 执行 rollback Hook（previous/current release 目录），幂等 |

## 参数（仅引用，不含 Secret 正文）

所有命令参数都是**引用/元数据**，绝不携带 Secret 正文、OSS 凭证或预签名 URL：

- `operation_id` / `target_id` / `node_id`：必填，操作与目标标识。
- `feature_key` / `release_id` / `config_hash`：Feature、Release、冻结配置哈希的引用。
- `secret_refs`：Secret 引用数组，每项仅含 `ref_id` / `object_key` / `version` /
  `content_hash` / `encryption_mode`；正文只存在于 OSS 对象与节点 `0600` 物化文件。
- manifest 的 JSON Schema 开启 `additionalProperties: false`，多余字段在控制面与节点两侧都会被拒绝。

## Runner 逻辑（V1）

1. **Preflight**：`repo.Layout.EnsureAll` 建全目录；若 `manifests/repository-manifest.json`
   存在则 `VerifyManifest`（逐对象 sha256/size，失败即失败），缺失则先执行一次 `sync`；
   最后 `FixPermissions`（repository secrets 目录 0700/文件 0600，`.servercli-local` 全 0700/0600）。
2. **sync**：读 `.servercli-local/credentials/oss-profile.json`（0600，绝不打印），
   `ListObjects` → 逐对象 `GetObject` 下载到 `repository/<rel>`（`ValidateRelPath`，跳过 `.servercli-local`），
   下载后 `VerifyManifest` + `FixPermissions`。凭证文件缺失报 `OSS credentials not provisioned`。
3. **install/update**：按 `feature_key`（可选 `release_version`）定位
   `repository/releases/<feature>/<version>/<sha256>/`，校验 bundle sha256，
   `ExtractTarGzip` 解压到 `.servercli-local/staging`（Limits：2000 文件 / 1GiB 总量 / 512MiB 单文件 / 1024 路径长度），
   `SwitchDir` 到 `.servercli-local/rendered/<feature>/<version>`；渲染 `runtime-config.yaml`（不写 Secret 正文），
   用 `PlaintextSecretProvider.Materialize` 把 `secret_refs` 物化到 `rendered/.../secrets/<ref_id>.yaml`（0600）；
   经 sudo wrapper 以固定参数执行 Hook。
4. **health-check**：执行 `health_hook`，退出码 0 → 成功，非 0 → 失败。
5. **backup**：执行 `backup_hook`，取 stdout 最后一行备份包路径（`/tmp/*.tar.gz`），
   计算 sha256/size，构造 `backups/<env>/<feature>/<node_id>/<yyyy>/<mm>/<dd>/<operation_id>/backup.tar.gz`
   精确前缀上传，`HeadObject` 校验 size，结果 `summary_json` 返回 `{object_key,size,sha256,backup_mode}`。
6. **rollback**：执行 `rollback_hook`（`--previous-release-dir` / `--current-release-dir`），幂等。

## 安全约束

- Hook 一律经固定 wrapper `/usr/local/libexec/servercli-deploy-wrapper` 以 root 执行；
  wrapper 缺失即失败，**绝不 fallback 到裸 root shell**。
- Hook 参数为固定白名单（`--feature-key` / `--node-id` / `--operation-id` / `--config-dir`
  / `--data-dir` / `--deployment-root-dir` 等），绝不拼接任意字符串。
- 取消经 `exec.CommandContext` 终止子进程；超时/取消映射为 `timed_out` / `cancelled`。
- 日志、事件、Result 均不打印 Secret 正文、OSS 凭证与签名串；Hook 输出在写入前过滤
  `LTAI...` / `AKID...` 等 AK 模式。
