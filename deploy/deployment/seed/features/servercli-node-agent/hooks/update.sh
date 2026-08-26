#!/usr/bin/env bash
# =============================================================================
# Hook: update — ServerCLI Node Agent (servercli-node-agent)
# 更新 = 幂等重装（版本变化时下载新制品并重启服务）。
# =============================================================================
set -euo pipefail
exec "$(dirname "$0")/install.sh" "$@"
