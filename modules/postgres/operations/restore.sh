#!/bin/sh
set -euo pipefail
# postgres restore: explicit backup id only; restore is an explicit high-risk
# operation and never automatic. The backup is read from $SERVERCLI_BACKUP_DIR
# (ops-provided) under the ops backup id.

if [ -z "${SERVERCLI_BACKUP_ID:-}" ]; then
  echo "postgres restore: explicit BACKUP_ID required" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "postgres restore: must run as root" >&2
  exit 1
fi
CONTAINER="servercli-postgres"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "postgres restore: container missing" >&2
  exit 1
fi
BACKUP_DIR="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/postgres}"
src="$BACKUP_DIR/$SERVERCLI_BACKUP_ID.sql"
if [ ! -f "$src" ]; then
  echo "postgres restore: backup not found: $SERVERCLI_BACKUP_ID" >&2
  exit 1
fi
DB_NAME="${SERVERCLI_CFG_DB_NAME:?DB_NAME required}"
docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -d '"$DB_NAME"' -v ON_ERROR_STOP=1 -q' < "$src" >/dev/null
echo "postgres restore: ok"
exit 0
