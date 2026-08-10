# ServerCLI 二进制安装说明

本包是面向物理主机（非 Docker）的自包含发布包，包含两个 Go 二进制、
前端静态资源、命令 manifest 与 systemd 部署模板，安装过程不需要 Go/Node。

## 1. 解压到部署目录

```bash
sudo mkdir -p /opt/servercli
sudo tar -xzf servercli-*.tar.gz -C /opt/servercli --strip-components=1
```

## 2. 准备环境配置

```bash
# 非 Secret 配置（以正式主节点为例）
sudo cp /opt/servercli/deploy/environments/production/production-primary.env.example \
        /opt/servercli/deploy/environments/production/production-primary.env

# Secret 配置（权限必须 0600）：填写 DATABASE_URL / ADMIN_INITIAL_PASSWORD 等
sudo cp /opt/servercli/deploy/environments/production/production-primary.secrets.env.example \
        /opt/servercli/deploy/environments/production/production-primary.secrets.env
sudo chmod 600 /opt/servercli/deploy/environments/production/production-primary.secrets.env
```

## 3. 安装 systemd 服务（推荐）

```bash
sudo useradd --system --home /opt/servercli --shell /usr/sbin/nologin servercli 2>/dev/null || true

sudo install -m 0644 /opt/servercli/deploy/systemd/servercli-control-plane.service.example \
        /etc/systemd/system/servercli-control-plane@production-production-primary.service
sudo install -m 0644 /opt/servercli/deploy/systemd/servercli-node-agent.service.example \
        /etc/systemd/system/servercli-node-agent@production-production-primary.service

sudo systemctl daemon-reload
sudo systemctl enable --now servercli-control-plane@production-production-primary.service
sudo systemctl enable --now servercli-node-agent@production-production-primary.service
```

注意：systemd 模板中的 `<ENV>/<INSTANCE>` 占位符与文件内容需按实际环境替换。

## 4. 直接运行（不依赖 systemd）

```bash
cd /opt/servercli
./bin/servercli-control-plane          # 控制面（前端 9044/9042，后端 9045/9043）
./bin/servercli-node-agent             # 子节点 Agent（主动外连主控）
```

健康检查：`curl http://127.0.0.1:9045/health/live`、`/health/ready`、`/version`。
正式环境必须使用受信任 TLS，禁止 `HTTP_INSECURE_SKIP_VERIFY=true`。

## 5. 使用 servercli CLI 安装器（推荐用于全新节点）

对全新的 CentOS/RHEL（EL8/EL9，x86_64/aarch64）物理主机，推荐使用一键安装器
`deploy/install-servercli.sh`：它下载签名 Release Manifest（GitHub 主源 + OSS 回退源）、
用发布公钥校验 Ed25519 签名、按 Manifest 逐个校验并安装三个二进制
（`servercli` / `servercli-control-plane` / `servercli-node-agent`）、公共模块
`modules/`、模板与 Schema，并原子切换 `/opt/servercli/current`、`/opt/servercli/previous`。

```bash
# 1) 以 root 运行；先准备发布公钥文件（Ed25519 PKIX PEM）
sudo bash deploy/install-servercli.sh --pubkey /path/to/release.pub \
    --version v1.2.3

# 2) 非 root 会直接报错；未传 --pubkey 时使用内嵌占位公钥并打印替换警告，
#    此时验签必然失败（fail-closed），生产必须提供真实公钥。

# 3) 默认 --version releases/latest（GitHub 最新版）；安装完成后仅当终端为 TTY
#    才询问是否运行 `servercli init`；非交互环境只安装不等待。
#    自动运行 init：加 --yes；完全跳过：加 --no-init-prompt。
```

常用参数：

| 参数 | 说明 |
| --- | --- |
| `--version` | 下载版本，默认 `releases/latest`，也可传 tag（如 `v1.2.3`） |
| `--github-base` | GitHub Release 下载基地址（默认 `https://github.com/inoriyuuuki/serverCli/releases/download`） |
| `--oss-base` | 可选 OSS 回退源基地址（GitHub 下载失败时使用） |
| `--pubkey` | 发布公钥文件（Ed25519 PKIX PEM）；缺省使用内嵌占位公钥（必须替换） |
| `--yes` | 安装完成后直接运行 `servercli init`，不再询问 |
| `--no-init-prompt` | 安装完成后不询问、不运行 `servercli init` |

信任模型：Manifest 内 `artifacts[].sha256` 摘要表为唯一信任锚，安装器对所有
下载产物逐个 `sha256sum -c` 校验；签名或摘要校验失败即退出（exit 4）。
详细设计见 `doc/13_INIT_AND_BOOTSTRAP.md`。

安装布局：

```text
/opt/servercli/
├── releases/<version>/   # 每个版本完整内容（bin/、modules/、templates/、schema/、deploy/）
├── current -> releases/<version>     # 软链接，staging + rename 原子切换
└── previous -> releases/<上一个版本>
```

安装后可通过稳定命令接口使用：

```bash
/opt/servercli/current/bin/servercli version
/opt/servercli/current/bin/servercli init status
/opt/servercli/current/bin/servercli ops update
```
