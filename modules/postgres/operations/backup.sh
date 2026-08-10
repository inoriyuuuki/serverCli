#!/bin/sh
set -euo pipefail
# postgres backup: logical dump written into $SERVERCLI_BACKUP_DIR (provided by
# `servercli ops backup`) using the ops-generated backup id. The password is
# read inside the container from the read-only /run/secrets mount, never argv.

if [ "$(id -u)" -ne 0 ]; then
  echo "postgres backup: must run as root" >&2
  exit 1
fi
CONTAINER="servercli-postgres"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "postgres backup: container missing" >&2
  exit 1
fi
DB_NAME="${SERVERCLI_CFG_DB_NAME:?DB_NAME required}"

BACKUP_DIR="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/postgres}"
BACKUP_ID="${SERVERCLI_BACKUP_ID:-pg-$(date -u +%Y%m%dT%H%M%SZ)}"
umask 077
mkdir -p "$BACKUP_DIR"
docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec pg_dump -U postgres -d '"$DB_NAME"' --no-owner --no-privileges' \
  > "$BACKUP_DIR/$BACKUP_ID.sql"
chmod 600 "$BACKUP_DIR/$BACKUP_ID.sql"
echo "postgres backup: $BACKUP_DIR/$BACKUP_ID.sql"
exit 0
