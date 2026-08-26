#!/usr/bin/env bash
# =============================================================================
# Hook: rollback — DataDashboard (data-dashboard)
# 幂等恢复上一 release 目录：current/previous 互换（无 compose 重启）。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""; DEPLOYMENT_ROOT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    -h|--help) sed -n '1,28p' "$0"; exit 0 ;;
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
echo "[rollback] OK: current -> ${prev}（data-dashboard）"
exit 0
