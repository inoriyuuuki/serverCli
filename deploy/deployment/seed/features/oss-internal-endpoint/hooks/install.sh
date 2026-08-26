#!/usr/bin/env bash
# =============================================================================
# Hook: install — OSS Internal Endpoint (oss-internal-endpoint)
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

DEFAULT_ENDPOINT="oss-cn-hangzhou-internal.aliyuncs.com"
DEFAULT_REGION="cn-hangzhou"
OSS_REGION="${OSS_REGION:-$(read_cfg oss_region)}"
OSS_REGION="${OSS_REGION:-$DEFAULT_REGION}"
OSS_ENDPOINT="${OSS_ENDPOINT:-$(read_cfg oss_endpoint)}"
OSS_ENDPOINT="${OSS_ENDPOINT:-$DEFAULT_ENDPOINT}"
OSS_BUCKET="${OSS_BUCKET:-$(read_cfg oss_bucket)}"
OSS_PREFIX="${OSS_PREFIX:-$(read_cfg oss_prefix)}"

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

mkdir -p "$DATA_DIR" "$CONFIG_DIR" "$DEPLOYMENT_ROOT_DIR/etc"

# 写内网 Endpoint 配置标记（非 Secret，0644）
conf_file="$DEPLOYMENT_ROOT_DIR/etc/oss-internal-endpoint.conf"
cat > "$conf_file" <<'CONF_EOF'
# OSS 内网 Endpoint 配置（由 oss-internal-endpoint feature 管理，幂等）
oss_region=cn-hangzhou
oss_endpoint=oss-cn-hangzhou-internal.aliyuncs.com
oss_bucket=your-oss-bucket
oss_prefix=deployment-repository/
CONF_EOF
chmod 0644 "$conf_file"
printf '%s\n' "${RELEASE_VERSION:-unknown}" > "$DEPLOYMENT_ROOT_DIR/etc/.oss-internal-endpoint.version"

echo "[install] OK: OSS 内网 Endpoint 配置已写入 $conf_file"
exit 0
