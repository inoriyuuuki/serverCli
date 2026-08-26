#!/usr/bin/env bash
# =============================================================================
# Hook: update — Docker Prerequisite (docker-prerequisite)
# 更新 = 幂等重装（确保 docker/compose 存在且可用）。
# =============================================================================
set -euo pipefail
exec "$(dirname "$0")/install.sh" "$@"
