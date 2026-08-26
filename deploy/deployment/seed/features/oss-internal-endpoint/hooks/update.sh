#!/usr/bin/env bash
# =============================================================================
# Hook: update — OSS Internal Endpoint (oss-internal-endpoint)
# 更新 = 幂等重写配置（配置内容变化时生效）。
# =============================================================================
set -euo pipefail
exec "$(dirname "$0")/install.sh" "$@"
