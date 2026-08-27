#!/usr/bin/env bash
# Hook: restore — 从备份恢复到数据目录（安全：数据已存在时需显式 force_delete）
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; DEPLOYMENT_ROOT_DIR=""; DATA_DIR=""; RESTORE_DIR=""; FORCE_DELETE="0"; RENDERED_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --operation-id) shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --restore-dir) RESTORE_DIR="${2:-}"; shift 2 ;;
    --force-delete) FORCE_DELETE="${2:-0}"; shift 2 ;;
    --rendered-dir) RENDERED_DIR="${2:-}"; shift 2 ;;
    -h|--help) sed -n '1,45p' "$0"; exit 0 ;;
    *) echo "[restore] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" && -n "$DATA_DIR" && -n "$RESTORE_DIR" ]] || { echo "[restore] 缺少必填参数（--feature-key/--node-id/--data-dir/--restore-dir）" >&2; exit 2; }
[[ -d "$RESTORE_DIR" ]] || { echo "[restore] 恢复源目录不存在: $RESTORE_DIR" >&2; exit 2; }

# 数据守卫：已有数据且未显式 force_delete → 拒绝（避免覆盖）
if [[ -d "$DATA_DIR" ]] && [[ -n "$(ls -A "$DATA_DIR" 2>/dev/null || true)" ]]; then
  if [[ "$FORCE_DELETE" != "1" ]]; then
    echo "[restore] FAIL: 数据目录 $DATA_DIR 已有数据；请先删除原数据，或显式 force_delete=1 后再恢复" >&2
    exit 2
  fi
  PREV="${DATA_DIR}.pre-restore-$(date +%Y%m%d%H%M%S)"
  mv "$DATA_DIR" "$PREV"
  echo "[restore] 原数据已迁移到 $PREV（未删除，可人工回收）" >&2
fi

mkdir -p "$DATA_DIR"
cp -a "$RESTORE_DIR"/. "$DATA_DIR"/
chmod -R u+rwX "$DATA_DIR" 2>/dev/null || true

# 重启服务（用当前 release 的 rendered compose）
if [[ -n "$RENDERED_DIR" && -f "$RENDERED_DIR/rendered/compose.yaml" ]]; then
  PROJECT="sc-${FEATURE_KEY}-${NODE_ID}"
  COMPOSE="docker compose"
  docker compose version >/dev/null 2>&1 || COMPOSE="docker-compose"
  if $COMPOSE -p "$PROJECT" -f "$RENDERED_DIR/rendered/compose.yaml" up -d; then
    echo "[restore] OK: $PROJECT 已用恢复数据重建"
  else
    echo "[restore] FAIL: compose up 失败（数据已恢复，请人工检查服务）" >&2
    exit 1
  fi
fi
echo "[restore] OK: ${FEATURE_KEY}@${NODE_ID} 数据已恢复"
exit 0
