#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — OSS Internal Endpoint (oss-internal-endpoint)
# 退出码 0=健康, 1=不健康。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""; DEPLOYMENT_ROOT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,26p' "$0"; exit 0 ;;
    *) echo "[health-check] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[health-check] 缺少 --feature-key/--node-id" >&2; exit 2; }
DEPLOYMENT_ROOT_DIR="${DEPLOYMENT_ROOT_DIR:-/opt/servercli-deployment}"
conf_file="$DEPLOYMENT_ROOT_DIR/etc/oss-internal-endpoint.conf"
[[ -f "$conf_file" ]] || { echo "[health-check] FAIL: $conf_file 不存在" >&2; exit 1; }
endpoint="$(sed -n 's/^oss_endpoint=//p' "$conf_file" | head -n1)"
if [[ -n "$endpoint" ]] && command -v getent >/dev/null 2>&1; then
  if ! getent hosts "$endpoint" >/dev/null 2>&1; then
    echo "[health-check] FAIL: 内网 Endpoint 无法解析 $endpoint" >&2
    exit 1
  fi
fi
echo "[health-check] OK: OSS 内网 Endpoint 配置存在（${endpoint}）"
exit 0
