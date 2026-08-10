#!/bin/sh
set -euo pipefail
# gitea preflight: postgres ready; data dir conflict detection.

if ! docker inspect servercli-postgres >/dev/null 2>&1 || \
   ! docker exec servercli-postgres pg_isready -U postgres >/dev/null 2>&1; then
  echo "gitea preflight: postgres not ready" >&2
  exit 1
fi
DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/gitea}"
if [ -d "$DATA_DIR" ] && [ -n "$(find "$DATA_DIR" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ] \
   && [ ! -f /var/lib/servercli/ownership/gitea.json ]; then
  echo "gitea preflight: data dir exists without servercli ownership; blocked (use adopt)" >&2
  exit 1
fi
echo "gitea preflight: ok"
exit 0
