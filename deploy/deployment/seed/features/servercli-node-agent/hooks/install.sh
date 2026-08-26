#!/usr/bin/env bash
# =============================================================================
# Hook: install — ServerCLI Node Agent (servercli-node-agent)
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

DEFAULT_AGENT_VERSION="0.2.0"
AGENT_VERSION="${AGENT_VERSION:-$(read_cfg agent_version)}"
AGENT_VERSION="${AGENT_VERSION:-$DEFAULT_AGENT_VERSION}"
RELEASE_URL="${RELEASE_URL:-$(read_cfg agent_release_url)}"
EXPECTED_SHA256="${EXPECTED_SHA256:-$(read_cfg agent_sha256)}"
INSTALL_DIR="${INSTALL_DIR:-/opt/servercli}"
BIN_DIR="$INSTALL_DIR/bin"

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

mkdir -p "$DATA_DIR" "$CONFIG_DIR" "$BIN_DIR"
[[ -n "$RELEASE_URL" ]] || { echo "[install] 缺少 agent_release_url（config）" >&2; exit 1; }

tmpd="$(mktemp -d /tmp/agent-install-XXXXXX)" || exit 1
trap 'rm -rf "$tmpd"' EXIT
echo "[install] 下载 ServerCLI Agent（${AGENT_VERSION}）..." >&2
curl -fsSL --connect-timeout 10 --max-time 600 -o "$tmpd/agent" "$RELEASE_URL"
if [[ -n "$EXPECTED_SHA256" ]]; then
  actual="$(sha256sum "$tmpd/agent" | awk '{print $1}')"
  if [[ "$actual" != "$EXPECTED_SHA256" ]]; then
    echo "[install] FAIL: Agent sha256 校验失败（期望 ${EXPECTED_SHA256}，实际 ${actual}）" >&2
    exit 1
  fi
  echo "[install] Agent sha256 校验通过" >&2
else
  echo "[install] 警告: 未提供 agent_sha256，跳过校验（生产必须提供）" >&2
fi

install -m 0755 "$tmpd/agent" "$BIN_DIR/servercli-node-agent"
printf '%s\n' "$AGENT_VERSION" > "$INSTALL_DIR/.agent_version"

# systemd 单元（幂等；<ENV>/<INSTANCE> 由控制面替换）
cat > /etc/systemd/system/servercli-node-agent.service <<'UNIT_EOF'
[Unit]
Description=ServerCLI Node Agent (servercli-node-agent)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=/opt/servercli/bin/servercli-node-agent
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT_EOF
systemctl daemon-reload
systemctl enable servercli-node-agent >/dev/null 2>&1 || true
systemctl start servercli-node-agent
sleep 2
if ! systemctl is-active --quiet servercli-node-agent; then
  echo "[install] FAIL: servercli-node-agent 未进入 active" >&2
  exit 1
fi
echo "[install] OK: servercli-node-agent@$AGENT_VERSION 已启动"
exit 0
