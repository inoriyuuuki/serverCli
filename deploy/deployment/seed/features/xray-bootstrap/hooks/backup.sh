#!/usr/bin/env bash
# =============================================================================
# Hook: backup — Xray Bootstrap (xray-bootstrap)
# backup_mode=none：本 feature 不提供数据备份（no-op，退出 0）。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    *) echo "[backup] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
echo "[backup] backup_mode=none：xray-bootstrap 不提供数据备份（幂等 no-op）" >&2
exit 0
