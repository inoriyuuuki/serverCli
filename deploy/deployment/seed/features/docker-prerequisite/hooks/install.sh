#!/usr/bin/env bash
# =============================================================================
# Hook: install — Docker Prerequisite (docker-prerequisite)
# 由 ServerCLI Agent Runner 以固定参数调用；幂等可重跑。
#
# 安全约束（本文件必须遵守）:
#   * 只接受固定参数，未知参数直接报错退出（不接收任意 shell 字符串参数）
#   * 不 source 任何全局 secrets.sh；敏感值由 Runner 经固定参数/配置文件注入
#   * 无 MAC 判断逻辑
#   * 镜像/制品 tag 默认固定写死；config 文件存在时 config 优先
#   * 不打印任何 Secret 正文
# =============================================================================
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; ENVIRONMENT_ID=""; HOSTNAME=""; RELEASE_VERSION=""
DEPLOYMENT_ROOT_DIR=""; OPERATION_ID=""; DATA_DIR=""; CONFIG_DIR=""; RENDERED_DIR=""
CONFIG_FILE=""; RELEASE_DIR=""; IMAGE_TAG=""; PORT=""

# ---- 固定参数说明 ----
# 支持参数（固定）：--feature-key --node-id --environment-id --hostname
#   --release-version --deployment-root-dir --operation-id --data-dir
#   --config-dir --rendered-dir --config-file --release-dir --image-tag --port
# 未知参数一律报错退出；不接收任意 shell 字符串参数。

while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --environment-id) ENVIRONMENT_ID="${2:-}"; shift 2 ;;
    --hostname) HOSTNAME="${2:-}"; shift 2 ;;
    --release-version) RELEASE_VERSION="${2:-}"; shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    --operation-id) OPERATION_ID="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --config-dir) CONFIG_DIR="${2:-}"; shift 2 ;;
    --rendered-dir) RENDERED_DIR="${2:-}"; shift 2 ;;
    --config-file) CONFIG_FILE="${2:-}"; shift 2 ;;
    --release-dir) RELEASE_DIR="${2:-}"; shift 2 ;;
    --image-tag) IMAGE_TAG="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    -h|--help) sed -n '1,45p' "$0"; exit 0 ;;
    *) echo "[install] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done

for _v in FEATURE_KEY NODE_ID DEPLOYMENT_ROOT_DIR; do
  if [[ -z "${!_v:-}" ]]; then
    echo "[install] 缺少必填参数 --${_v}（feature-key/node-id/deployment-root-dir 必填）" >&2
    exit 2
  fi
done

DATA_DIR="${DATA_DIR:-$DEPLOYMENT_ROOT_DIR/data/$FEATURE_KEY}"
CONFIG_DIR="${CONFIG_DIR:-$DEPLOYMENT_ROOT_DIR/configs/$FEATURE_KEY}"
RENDERED_DIR="${RENDERED_DIR:-$DEPLOYMENT_ROOT_DIR/rendered/$FEATURE_KEY}"
CONFIG_FILE="${CONFIG_FILE:-$CONFIG_DIR/config.yaml}"

# ---- 从 config 读取（约定：Agent Runner 将合并后的 feature 配置写入 CONFIG_FILE）----
read_cfg() {
  local key="$1" file="${2:-$CONFIG_FILE}" val=""
  [[ -f "$file" ]] || return 0
  val="$(sed -n "s/^[[:space:]]*${key}:[[:space:]]*[\"']\?\([^\"'[:space:]]*\).*/\1/p" "$file" | head -n1)"
  printf '%s' "$val"
}

DEFAULT_DOCKER_VERSION="28.0.0"
DOCKER_VERSION="${DOCKER_VERSION:-$(read_cfg docker_version)}"
DOCKER_VERSION="${DOCKER_VERSION:-$DEFAULT_DOCKER_VERSION}"
DOWNLOAD_BASE="${DOWNLOAD_BASE:-$(read_cfg download_base_url)}"
DOWNLOAD_PROXY="${DOWNLOAD_PROXY:-$(read_cfg download_proxy)}"

# ---- release 目录管理（current/previous 符号链接；幂等）----
# 布局: $DEPLOYMENT_ROOT_DIR/releases/<feature_key>/<version>/ (bundle 解包目录)
#       .../current   -> 当前生效 release 目录
#       .../previous  -> 上一 release 目录（供 rollback）
if [[ -n "$RELEASE_DIR" && -n "$RELEASE_VERSION" ]]; then
  rel_root="$DEPLOYMENT_ROOT_DIR/releases/$FEATURE_KEY"
  mkdir -p "$rel_root"
  cur="$(readlink "$rel_root/current" 2>/dev/null || true)"
  if [[ "$cur" != "$RELEASE_DIR" ]]; then
    if [[ -n "$cur" ]]; then
      rm -f "$rel_root/previous"
      ln -s "$cur" "$rel_root/previous"
    fi
    rm -f "$rel_root/current"
    ln -s "$RELEASE_DIR" "$rel_root/current"
    echo "[install] current release -> $RELEASE_DIR" >&2
  else
    echo "[install] release 未变化（current=${cur}），幂等跳过" >&2
  fi
  RENDERED_DIR="$RELEASE_DIR/rendered"
fi
mkdir -p "$RENDERED_DIR"

mkdir -p "$DATA_DIR" "$CONFIG_DIR"

curl_extra=()
if [[ -n "$DOWNLOAD_PROXY" ]]; then curl_extra=(-x "$DOWNLOAD_PROXY"); fi

# 1) docker 引擎（幂等）
if command -v docker >/dev/null 2>&1; then
  echo "[install] docker 已安装，跳过" >&2
else
  [[ -n "$DOWNLOAD_BASE" ]] || { echo "[install] 缺少 download_base_url（config）" >&2; exit 1; }
  tmpd="$(mktemp -d /tmp/docker-install-XXXXXX)" || exit 1
  trap 'rm -rf "$tmpd"' EXIT
  url="${DOWNLOAD_BASE%/}/docker-${DOCKER_VERSION}.tgz"
  echo "[install] 下载 docker 静态包 $url ..." >&2
  curl "${curl_extra[@]}" -fsSL --connect-timeout 10 --max-time 600 -o "$tmpd/docker.tgz" "$url"
  tar xzf "$tmpd/docker.tgz" -C "$tmpd"
  install -m 0755 "$tmpd"/docker/docker "$tmpd"/docker/dockerd "$tmpd"/docker/docker-init \
      "$tmpd"/docker/containerd "$tmpd"/docker/containerd-shim-runc-v2 "$tmpd"/docker/runc \
      "$tmpd"/docker/ctr "$tmpd"/docker/docker-proxy /usr/local/bin/
  cat > /etc/systemd/system/docker.service <<'UNIT_EOF'
[Unit]
Description=Docker Application Container Engine
Documentation=https://docs.docker.com
After=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/dockerd
ExecReload=/bin/kill -s HUP $MAINPID
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TimeoutStartSec=0
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT_EOF
  cat > /etc/systemd/system/docker.socket <<'SOCK_EOF'
[Unit]
Description=Docker Socket for the API

[Socket]
ListenStream=/var/run/docker.sock
SocketMode=0660
SocketUser=root
SocketGroup=docker

[Install]
WantedBy=sockets.target
SOCK_EOF
  systemctl daemon-reload
  systemctl enable docker.socket docker.service
  systemctl start docker.socket docker.service
fi

# 2) docker compose 插件（幂等）
COMPOSE_PLUGIN="/usr/local/lib/docker/cli-plugins/docker-compose"
if docker compose version >/dev/null 2>&1 || [[ -x "$COMPOSE_PLUGIN" ]]; then
  echo "[install] docker compose 已就绪，跳过" >&2
else
  [[ -n "$DOWNLOAD_BASE" ]] || { echo "[install] 缺少 download_base_url（config）" >&2; exit 1; }
  tmpd2="$(mktemp -d /tmp/compose-install-XXXXXX)" || exit 1
  trap 'rm -rf "$tmpd2"' EXIT
  url="${DOWNLOAD_BASE%/}/docker-compose-linux-x86_64"
  echo "[install] 下载 docker compose 插件 $url ..." >&2
  curl "${curl_extra[@]}" -fsSL --connect-timeout 10 --max-time 600 -o "$tmpd2/docker-compose" "$url"
  mkdir -p /usr/local/lib/docker/cli-plugins
  install -m 0755 "$tmpd2/docker-compose" "$COMPOSE_PLUGIN"
fi

systemctl daemon-reload
systemctl enable docker >/dev/null 2>&1 || true
systemctl start docker
docker info >/dev/null 2>&1 || { echo "[install] FAIL: docker info 失败" >&2; exit 1; }
echo "[install] OK: docker + compose 就绪 (docker=$DOCKER_VERSION)"
exit 0
