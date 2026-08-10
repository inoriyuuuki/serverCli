#!/bin/sh
set -euo pipefail
# control-plane plan: postgres + caddy gate, production PostgreSQL enforced.

echo "plan: start control plane after postgres + caddy gateway ready"
echo "plan: production database: postgres (${SERVERCLI_CFG_DB_HOST:-servercli-postgres}:${SERVERCLI_CFG_DB_PORT:-5432}/${SERVERCLI_CFG_DB_NAME:-<db>})"
echo "plan: reachable via caddy on the dedicated bridge network"
echo "plan: record control_plane_local_ready after all health endpoints pass"
exit 0
