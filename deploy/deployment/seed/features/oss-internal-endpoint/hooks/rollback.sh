#!/usr/bin/env bash
# =============================================================================
# Hook: rollback — OSS Internal Endpoint (oss-internal-endpoint)
# rollback_capability=false：本 feature 不支持回滚（no-op，退出 0）。
# =============================================================================
set -euo pipefail
echo "[rollback] rollback_capability=false：oss-internal-endpoint 不支持回滚（幂等 no-op）" >&2
exit 0
