#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — Docker Prerequisite (docker-prerequisite)
# 退出码 0=健康, 1=不健康。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,24p' "$0"; exit 0 ;;
    *) echo "[health-check] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[health-check] 缺少 --feature-key/--node-id" >&2; exit 2; }
command -v docker >/dev/null 2>&1 || { echo "[health-check] FAIL: docker 未安装" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "[health-check] FAIL: docker daemon 不可用" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "[health-check] FAIL: docker compose 不可用" >&2; exit 1; }
echo "[health-check] OK: docker + compose 可用"
exit 0
