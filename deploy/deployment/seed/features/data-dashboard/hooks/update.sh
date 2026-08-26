#!/usr/bin/env bash
# =============================================================================
# Hook: update — DataDashboard (data-dashboard)
# 更新 = 幂等重装（新 release 制品 + 重启服务）。
# =============================================================================
set -euo pipefail
exec "$(dirname "$0")/install.sh" "$@"
