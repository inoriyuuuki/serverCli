#!/usr/bin/env bash
# =============================================================================
# Hook: health-check — Xray Bootstrap (xray-bootstrap)
# 通过 127.0.0.1:10809 代理探活（退出码 0=健康, 1=不健康）。
# =============================================================================
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; PROBE_URL="https://github.com"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --probe-url) PROBE_URL="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,28p' "$0"; exit 0 ;;
    *) echo "[health-check] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[health-check] 缺少 --feature-key/--node-id" >&2; exit 2; }

[[ -x /usr/local/bin/xray ]] || { echo "[health-check] FAIL: xray 未安装" >&2; exit 1; }
code="$(curl -x http://127.0.0.1:10809 -fsS -o /dev/null -w '%{http_code}' --connect-timeout 5 --max-time 10 "$PROBE_URL" 2>/dev/null || true)"
if [[ "$code" != "200" ]]; then
  echo "[health-check] FAIL: 代理探活未通过（$PROBE_URL -> ${code:-无响应}）" >&2
  exit 1
fi
echo "[health-check] OK: xray 代理可用（$PROBE_URL -> 200）"
exit 0
