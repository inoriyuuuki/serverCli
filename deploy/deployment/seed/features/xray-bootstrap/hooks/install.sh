#!/usr/bin/env bash
# =============================================================================
# Hook: install — Xray Bootstrap (xray-bootstrap)
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

# 制品/版本：默认固定写死；config 存在时 config 优先
DEFAULT_VERSION="1.8.24"
XRAY_VERSION="${XRAY_VERSION:-$DEFAULT_VERSION}"
cfg_ver="$(read_cfg version)"
[[ -n "$cfg_ver" ]] && XRAY_VERSION="$cfg_ver"
ARTIFACT_URL="${ARTIFACT_URL:-$(read_cfg artifact_url)}"
PROBE_URL="${PROBE_URL:-$(read_cfg probe_url)}"
PROBE_URL="${PROBE_URL:-https://github.com}"
XRAY_BIN="${XRAY_BIN:-/usr/local/bin/xray}"
XRAY_ETC="${XRAY_ETC:-/usr/local/etc/xray}"

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

mkdir -p "$DATA_DIR" "$CONFIG_DIR" "$XRAY_ETC"

need_download=1
if [[ -x "$XRAY_BIN" && -f "$XRAY_ETC/.xray_version" ]]; then
  have="$(cat "$XRAY_ETC/.xray_version" 2>/dev/null || true)"
  if [[ "$have" == "$XRAY_VERSION" ]]; then
    need_download=0
    echo "[install] xray 已安装（${XRAY_VERSION}），幂等跳过下载" >&2
  fi
fi

if [[ "$need_download" == "1" ]]; then
  [[ -n "$ARTIFACT_URL" ]] || { echo "[install] 缺少 artifact_url（config 或固定默认均未提供）" >&2; exit 1; }
  command -v unzip >/dev/null 2>&1 || { echo "[install] 缺少 unzip（yum install -y unzip）" >&2; exit 1; }
  tmpd="$(mktemp -d /tmp/xray-install-XXXXXX)" || exit 1
  trap 'rm -rf "$tmpd"' EXIT
  echo "[install] 下载 xray 制品（版本 ${XRAY_VERSION}）..." >&2
  curl -fsSL --connect-timeout 10 --max-time 300 -o "$tmpd/xray.zip" "$ARTIFACT_URL"
  unzip -oq "$tmpd/xray.zip" -d "$tmpd/x"
  [[ -f "$tmpd/x/xray" ]] || { echo "[install] 制品中未找到 xray 二进制" >&2; exit 1; }
  install -m 0755 "$tmpd/x/xray" "$XRAY_BIN"
  # geosite/geoip 数据（如制品内含则一并安装）
  [[ -f "$tmpd/x/geosite.dat" ]] && install -m 0644 "$tmpd/x/geosite.dat" "$XRAY_ETC/geosite.dat"
  [[ -f "$tmpd/x/geoip.dat" ]] && install -m 0644 "$tmpd/x/geoip.dat" "$XRAY_ETC/geoip.dat"
  printf '%s\n' "$XRAY_VERSION" > "$XRAY_ETC/.xray_version"
fi

# config.json：config-dir 提供则使用，否则写入默认（socks 10808 / http 10809）
if [[ -f "$CONFIG_DIR/config.json" ]]; then
  install -m 0644 "$CONFIG_DIR/config.json" "$XRAY_ETC/config.json"
elif [[ ! -f "$XRAY_ETC/config.json" ]]; then
  cat > "$XRAY_ETC/config.json" <<'JSON_EOF'
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    { "port": 10808, "protocol": "socks", "settings": { "auth": "noauth" }, "tag": "socks-in" },
    { "port": 10809, "protocol": "http", "settings": {}, "tag": "http-in" }
  ],
  "outbounds": [ { "protocol": "freedom", "tag": "direct" } ]
}
JSON_EOF
fi

# systemd 单元（幂等）
cat > /etc/systemd/system/xray.service <<'UNIT_EOF'
[Unit]
Description=Xray Service (xray-bootstrap)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/xray run -c /usr/local/etc/xray/config.json
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT_EOF
systemctl daemon-reload
systemctl enable xray >/dev/null 2>&1 || true
systemctl start xray

# 代理探活：失败返回非零停止（最长等待 120s）
echo "[install] 等待 xray 代理就绪（探活 ${PROBE_URL}）..." >&2
ok=0
for _i in $(seq 1 24); do
  code="$(curl -x http://127.0.0.1:10809 -fsS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 10 "$PROBE_URL" 2>/dev/null || true)"
  if [[ "$code" == "200" ]]; then ok=1; break; fi
  sleep 5
done
if [[ "$ok" != "1" ]]; then
  echo "[install] FAIL: xray 探活未通过（${PROBE_URL}），停止" >&2
  exit 1
fi
echo "[install] OK: xray@$XRAY_VERSION 代理就绪（$PROBE_URL -> 200）"
exit 0
