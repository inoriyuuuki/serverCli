#!/usr/bin/env bash
# =============================================================================
# Hook: install — HedgeDoc (hedgedoc)
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

# 镜像 tag：默认固定写死；config 存在时 config 优先（生产应锁定 digest）
DEFAULT_IMAGE_TAG="quay.io/hedgedoc/hedgedoc:1.10.7"
IMAGE_TAG="${IMAGE_TAG:-$DEFAULT_IMAGE_TAG}"
cfg_image_tag="$(read_cfg image_tag)"
[[ -n "$cfg_image_tag" ]] && IMAGE_TAG="$cfg_image_tag"
cfg_digest="$(read_cfg image_digest)"
[[ -n "$cfg_digest" ]] && IMAGE_TAG="$cfg_digest"
PORT="${PORT:-$(read_cfg service_port)}"
PORT="${PORT:-3000}"

command -v docker >/dev/null 2>&1 || { echo "[install] 缺少 docker，请先安装 docker-prerequisite feature" >&2; exit 1; }

compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    printf '%s' "docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    printf '%s' "docker-compose"
  else
    printf '%s' ""
  fi
}

COMPOSE="$(compose_cmd)"
[[ -n "$COMPOSE" ]] || { echo "[install] 缺少 docker compose / docker-compose" >&2; exit 1; }

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

# 渲染 compose.yaml（固定变量模板，无用户任意输入）
cat > "$RENDERED_DIR/compose.yaml" <<COMPOSE_EOF
services:
  hedgedoc:
    image: quay.io/hedgedoc/hedgedoc:1.10.7
    container_name: sc-hedgedoc-${NODE_ID}
    restart: unless-stopped
    ports:
      - "3000:3000"
    volumes:
      - ${DATA_DIR}/uploads:/hedgedoc/public/uploads
    env_file:
      - ${RENDERED_DIR}/.env

COMPOSE_EOF

# 渲染 .env：config-dir 已放置 .env 则复用；否则生成占位（真实 Secret 由控制面注入）
if [[ -f "$CONFIG_DIR/.env" ]]; then
  cp -f "$CONFIG_DIR/.env" "$RENDERED_DIR/.env"
else
  cat > "$RENDERED_DIR/.env" <<'ENV_EOF'
# 占位 .env（hedgedoc）：真实 Secret 由控制面注入
# V1 明文存私有 OSS，禁止提交真实值；.example 文件仅含占位键名
ENV_EOF
fi

printf '%s\n' "$IMAGE_TAG" > "$RENDERED_DIR/.image_tag"
printf '%s\n' "${RELEASE_VERSION:-unknown}" > "$RENDERED_DIR/.release_version"

PROJECT="sc-${FEATURE_KEY}-${NODE_ID}"
if $COMPOSE -p "$PROJECT" -f "$RENDERED_DIR/compose.yaml" up -d; then
  echo "[install] OK: $PROJECT 已就绪 (image=$IMAGE_TAG)"
else
  echo "[install] 失败: $COMPOSE up -d 返回非零 (image=$IMAGE_TAG)" >&2
  exit 1
fi
exit 0
