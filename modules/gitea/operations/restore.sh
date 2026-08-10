#!/bin/sh
set -euo pipefail
# gitea restore: explicit backup id only; restore is explicit, never automatic.

if [ -z "${SERVERCLI_BACKUP_ID:-}" ]; then
  echo "gitea restore: explicit BACKUP_ID required" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "gitea restore: must run as root" >&2
  exit 1
fi
archive="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/gitea}/$SERVERCLI_BACKUP_ID"
if [ ! -f "$archive" ]; then
  echo "gitea restore: backup not found: $SERVERCLI_BACKUP_ID" >&2
  exit 1
fi
umask 077
tmp="$(mktemp -d /run/gitea-restore.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$archive" -C "$tmp"
rm -rf /var/lib/servercli/gitea
mv "$tmp/gitea" /var/lib/servercli/gitea
echo "gitea restore: ok (restart gitea to apply)"
exit 0
