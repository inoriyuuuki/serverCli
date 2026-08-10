#!/bin/sh
set -euo pipefail
# caddy backup: archive config and ACME data.

if [ "$(id -u)" -ne 0 ]; then
  echo "caddy backup: must run as root" >&2
  exit 1
fi
if [ ! -d /var/lib/servercli/caddy ]; then
  echo "caddy backup: nothing to back up"
  exit 0
fi
backup_dir="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/caddy}"
mkdir -p "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$backup_dir/${SERVERCLI_BACKUP_ID:-caddy-$stamp}.tar.gz"
tar -czf "$archive" -C /var/lib/servercli caddy
chmod 600 "$archive"
echo "caddy backup: $archive"
exit 0
