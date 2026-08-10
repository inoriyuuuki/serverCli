#!/bin/sh
set -euo pipefail
# gitea backup: data dir archive; when postgres-backed, include a logical dump.

if [ "$(id -u)" -ne 0 ]; then
  echo "gitea backup: must run as root" >&2
  exit 1
fi
if [ ! -d /var/lib/servercli/gitea ]; then
  echo "gitea backup: nothing to back up"
  exit 0
fi
backup_dir="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/gitea}"
mkdir -p "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$backup_dir/${SERVERCLI_BACKUP_ID:-gitea-$stamp}.tar.gz"
tar -czf "$archive" -C /var/lib/servercli gitea
chmod 600 "$archive"
if [ -n "${SERVERCLI_CFG_DB_NAME:-}" ] && docker inspect servercli-postgres >/dev/null 2>&1; then
  if docker exec servercli-postgres sh -c 'test -r /run/secrets/super' >/dev/null 2>&1; then
    docker exec servercli-postgres sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec pg_dump -U postgres -d '"${SERVERCLI_CFG_DB_NAME}"' --no-owner --no-privileges' \
      > "$backup_dir/$stamp.sql" 2>/dev/null || true
    chmod 600 "$backup_dir/$stamp.sql" 2>/dev/null || true
  fi
fi
echo "gitea backup: $archive"
exit 0
