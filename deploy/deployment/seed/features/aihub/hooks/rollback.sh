#!/usr/bin/env bash
# =============================================================================
# Hook: rollback — AIHub (aihub)
# 幂等恢复上一 release 目录：current/previous 互换后按上一 release 的
# rendered 配置重启服务。由 Agent Runner 以固定参数调用。
# =============================================================================
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; DEPLOYMENT_ROOT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    -h|--help) sed -n '1,32p' "$0"; exit 0 ;;
    *) echo "[rollback] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" && -n "$DEPLOYMENT_ROOT_DIR" ]] || { echo "[rollback] 缺少 --feature-key/--node-id/--deployment-root-dir" >&2; exit 2; }

rel_root="$DEPLOYMENT_ROOT_DIR/releases/$FEATURE_KEY"
if [[ ! -L "$rel_root/previous" ]]; then
  echo "[rollback] 无上一 release 可回滚（幂等）" >&2
  exit 0
fi
prev="$(readlink "$rel_root/previous" 2>/dev/null || true)"
cur="$(readlink "$rel_root/current" 2>/dev/null || true)"
rm -f "$rel_root/current"
ln -s "$prev" "$rel_root/current"
if [[ -n "$cur" ]]; then
  rm -f "$rel_root/previous"
  ln -s "$cur" "$rel_root/previous"
else
  rm -f "$rel_root/previous"
fi
echo "[rollback] current -> $prev" >&2

prev_rendered="$prev/rendered"
[[ -f "$prev_rendered/compose.yaml" ]] || { echo "[rollback] 上一 release 无 rendered/compose.yaml，跳过重启" >&2; exit 0; }
command -v docker >/dev/null 2>&1 || { echo "[rollback] FAIL: 缺少 docker" >&2; exit 1; }

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
[[ -n "$COMPOSE" ]] || { echo "[rollback] FAIL: 缺少 docker compose" >&2; exit 1; }
PROJECT="sc-${FEATURE_KEY}-${NODE_ID}"
$COMPOSE -p "$PROJECT" -f "$prev_rendered/compose.yaml" up -d
echo "[rollback] OK: 已回滚到 $prev 并重启"
exit 0
