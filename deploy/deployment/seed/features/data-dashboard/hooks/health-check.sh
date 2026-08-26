#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — DataDashboard (data-dashboard)
# 退出码 0=健康, 1=不健康。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""; PORT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,26p' "$0"; exit 0 ;;
    *) echo "[health-check] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[health-check] 缺少 --feature-key/--node-id" >&2; exit 2; }
PORT="${PORT:-9080}"
UNIT_NAME="data-dashboard-${NODE_ID}"
if systemctl is-active --quiet "$UNIT_NAME" 2>/dev/null; then
  echo "[health-check] OK: $UNIT_NAME active (port=$PORT)"
  exit 0
fi
code="$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:${PORT}/" 2>/dev/null || true)"
if [[ -n "$code" ]]; then
  echo "[health-check] OK: port $PORT 响应 ($code)"
  exit 0
fi
echo "[health-check] FAIL: $UNIT_NAME 未运行且端口 $PORT 无响应"
exit 1
