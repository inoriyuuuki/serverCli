#!/usr/bin/env bash
# =============================================================================
# Hook: backup — DataDashboard (data-dashboard)
# 将数据目录打成 /tmp 下 tar.gz，并把包路径输出到 stdout 最后一行。
# =============================================================================
set -euo pipefail
FEATURE_KEY=""; NODE_ID=""; DATA_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --rendered-dir) shift 2 ;;
    -h|--help) sed -n '1,32p' "$0"; exit 0 ;;
    *) echo "[backup] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done
[[ -n "$FEATURE_KEY" && -n "$NODE_ID" ]] || { echo "[backup] 缺少 --feature-key/--node-id" >&2; exit 2; }
DATA_DIR="${DATA_DIR:-/opt/servercli-deployment/data/$FEATURE_KEY}"
mkdir -p "$DATA_DIR"
tmpf="$(mktemp /tmp/${FEATURE_KEY}-backup-XXXXXX)" || exit 1
rm -f "$tmpf"
backup_file="${tmpf}.tar.gz"
tar -czf "$backup_file" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"
echo "[backup] OK: ${FEATURE_KEY}@${NODE_ID} 数据已打包" >&2
echo "$backup_file"
exit 0
