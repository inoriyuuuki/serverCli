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
