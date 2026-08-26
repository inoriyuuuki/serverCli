# ServerCLI Deployment 种子仓库（seed）

本目录是**控制面导入 / 上传 OSS** 的权威内容模板（首个 Feature 种子），用于
节点引导（`scripts/deployment-bootstrap.sh`）从私有 OSS 同步后执行部署。

- 首批迁移服务：`caddy` / `memos` / `hedgedoc` / `lobehub` / `aihub` / `dbx` /
  `data-dashboard`
- 首批基础能力：`xray-bootstrap` / `docker-prerequisite` /
  `servercli-node-agent` / `oss-internal-endpoint`

> 版本：种子 v0.1.0（Feature 契约 `servercli/v1`）。
> 每个 Feature 提供 manifest + 5 个固定 hook + 一个占位 release 制品
> （真实 `bundle.tar.gz` + 真实 sha256）。

---

## 1. 目录结构

```
deploy/deployment/seed/
├── README.md                      # 本文件
├── catalog/
│   └── features.yaml              # Feature 目录索引（feature_key/name/version/summary）
├── configs/
│   ├── shared/<profile>.yaml      # 共享配置示例（端口/数据目录/镜像 tag 等）
│   └── nodes/<node_id>/<feature>.yaml   # 节点覆盖示例（aliyun-node-001 仅为示例）
├── features/
│   └── <feature_key>/
│       ├── manifest.yaml          # Feature Manifest（含 config_schema JSON Schema）
│       └── hooks/
│           ├── install.sh         # 幂等安装（固定参数）
│           ├── update.sh          # 更新（幂等）
│           ├── backup.sh          # 数据打包到 /tmp，路径输出到 stdout 最后一行
│           ├── health-check.sh    # 退出码 0=健康 / 1=不健康
│           └── rollback.sh        # 幂等恢复上一 release 目录
├── manifests/
│   └── repository-manifest.json   # 仓库级 Manifest：逐对象 path/size/sha256（不含自身）
├── releases/
│   └── <feature_key>/<version>/<sha256>/
│       ├── manifest.json          # release 清单（bundle sha256/size、files 逐文件 hash）
│       ├── bundle.tar.gz          # 真实制品（manifest.yaml + hooks/）
│       └── bundle.sig             # 占位签名（真实签名由控制面签发）
└── secrets/
    ├── shared/<profile>.secrets.yaml.example
    └── nodes/<node_id>/<feature>.secrets.yaml.example
```

## 2. Hook 契约（供 Agent Runner 调用）

所有 hook 均使用 `bash` + `set -euo pipefail`，**只接受固定参数**：

```
--feature-key --node-id --environment-id --hostname --release-version
--deployment-root-dir --operation-id --data-dir --config-dir --rendered-dir
--config-file --release-dir --image-tag --port
```

- 未知参数一律报错退出（**不接收任意 shell 字符串参数**）。
- 不 source 任何全局 `secrets.sh`；敏感值由 Runner 经固定参数/配置文件注入。
- 无 MAC 判断逻辑。
- 镜像/制品 tag 默认**固定写死**在脚本内；`config.yaml` 存在时 **config 优先**
  （生产建议锁定 `image_digest`）。
- `install` 幂等可重跑；`update` 复用 install（幂等）；`rollback` 幂等恢复
  `releases/<feature>/previous`。
- release 目录布局（由 hook 维护）：

```
<deployment_root_dir>/releases/<feature_key>/
    <version>/        # bundle 解包目录（含 rendered/ 渲染产物）
    current           # 符号链接 -> 当前生效 release
    previous          # 符号链接 -> 上一 release（供 rollback）
```

## 3. 如何导入 OSS

1. 由控制面将本 seed 目录同步到私有 OSS：

```bash
# 示例（真实 AK/SK 必须交互输入，禁止写在命令行/脚本里）
ossutil -c <0600-credential-file> sync deploy/deployment/seed/ \
    oss://<private-bucket>/deployment-repository/ --update
```

2. 节点首引导执行：

```bash
sudo bash scripts/deployment-bootstrap.sh \
    --bucket <private-bucket> --prefix deployment-repository/ \
    --region cn-hangzhou --endpoint oss-cn-hangzhou-internal.aliyuncs.com
```

   引导脚本会：交互输入 OSS AK/SK（`read`/`read -s`，临时凭证文件 0600）→
   同步到 `/opt/servercli-deployment/repository/` → 按
   `manifests/repository-manifest.json` 逐对象校验 sha256 → 修正 secrets 权限
   （目录 0700 / 文件 0600）→ 输出 Bucket/Prefix/对象数/下载状态/hash。

3. 后续步骤（由后续版本在 bootstrap 中实现，本种子仅预留注释）：
   安装 xray → 代理探活（127.0.0.1:10809 测 GitHub）→ 下载固定版本
   ServerCLI Agent（GitHub Release）→ 校验 sha256+签名 → 安装 systemd →
   复用现有 enrollment 注册（调用主控 `/api/v1/agent/enrollments`）→
   管理员审批后 node online。主控地址**直接访问，不走 xray**。

## 4. V1 明文 Secret 风险提示（必须阅读）

- V1 阶段 Secret 以**明文 YAML** 存放在**私有 OSS** 中（仅含键名 + 占位值）。
- 本仓库 `secrets/` 下**只允许存在 `.example` 模板**，禁止出现任何真实值。
- 真实 Secret 的存放路径、读取与注入由控制面负责，节点 hook 不直接读取 Secret
  文件，仅通过渲染后的 `config.yaml` / `.env` 获取。
- 任何真实 Secret 一旦进入 Git，**立即视为泄露**并轮换。

## 5. 禁止把 OSS 部署目录提交 Git

- `/opt/servercli-deployment/`（节点上的 OSS 同步目录）是运行时数据，**禁止**
  提交到任何 Git 仓库。
- 本 seed 仓库内若日后生成真实 Secret 文件（去掉 `.example` 后缀），也必须
  排除在 Git 之外；`.gitignore` 已对 `*.secrets.env` 等做兜底，但**不要依赖
  兜底**，应以「不生成真实文件到仓库」为原则。
- 同步/导入 OSS 使用独立的私有 Bucket（`<private-bucket>`），与公开代码仓库
  完全隔离。

## 6. 更新种子

1. 修改 `features/<key>/manifest.yaml` 或 `hooks/*.sh`；
2. 重新生成 release 制品（`bundle.tar.gz` + sha256 目录名 + `manifest.json` +
   `bundle.sig`），并同步更新 `catalog/features.yaml`；
3. 最后重新生成 `manifests/repository-manifest.json`（逐对象 path/size/sha256），
   保证与磁盘一致。
