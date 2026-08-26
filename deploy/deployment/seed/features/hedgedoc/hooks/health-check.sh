#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — HedgeDoc (hedgedoc)
# 退出码 0=健康, 1=不健康。由 Agent Runner 以固定参数调用。
# =============================================================================
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; PORT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,28p' "$0"; exit 0 ;;
    *) echo "[health-check] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[health-check] 缺少 --feature-key/--node-id" >&2; exit 2; }

command -v docker >/dev/null 2>&1 || { echo "[health-check] FAIL: 缺少 docker" >&2; exit 1; }
PROJECT="sc-${FEATURE_KEY}-${NODE_ID}"
if ! docker inspect -f '{{.State.Running}}' "$PROJECT" 2>/dev/null | grep -q true; then
  echo "[health-check] FAIL: 容器 $PROJECT 未运行" >&2
  exit 1
fi
if [[ -n "$PORT" && "$PORT" != "0" ]]; then
  code="$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 10 "http://127.0.0.1:${PORT}/" 2>/dev/null || true)"
  if [[ -z "$code" ]]; then
    echo "[health-check] FAIL: 端口 $PORT 无响应" >&2
    exit 1
  fi
fi
echo "[health-check] OK: $PROJECT (port=${PORT:-none})"
exit 0
