#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — ServerCLI Node Agent (servercli-node-agent)
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
if [[ -x /opt/servercli/bin/servercli-node-agent ]] && systemctl is-active --quiet servercli-node-agent 2>/dev/null; then
  echo "[health-check] OK: servercli-node-agent 运行中"
  exit 0
fi
if pgrep -f servercli-node-agent >/dev/null 2>&1; then
  echo "[health-check] OK: servercli-node-agent 进程存在"
  exit 0
fi
echo "[health-check] FAIL: servercli-node-agent 未运行"
exit 1
