#!/bin/sh
set -euo pipefail
# gitea plan: fresh installs use Foundation PostgreSQL; adopted legacy
# instances keep their existing database backend.

backend="${SERVERCLI_CFG_BACKEND:-postgres}"
echo "plan: install gitea (backend=$backend)"
if [ "$backend" = "postgres" ]; then
  echo "plan: dedicated database on Foundation PostgreSQL (${SERVERCLI_CFG_DB_NAME:-<db>})"
else
  echo "plan: adopt existing instance; keep existing database (no implicit migration)"
fi
echo "plan: gitea is not part of the core_ready hard gate"
exit 0
