#!/bin/sh
set -euo pipefail
# control-plane backup: archive state dir.

if [ "$(id -u)" -ne 0 ]; then
  echo "control-plane backup: must run as root" >&2
  exit 1
fi
if [ ! -d /var/lib/servercli/control-plane ]; then
  echo "control-plane backup: nothing to back up"
  exit 0
fi
backup_dir="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/control-plane}"
mkdir -p "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$backup_dir/${SERVERCLI_BACKUP_ID:-control-plane-$stamp}.tar.gz"
tar -czf "$archive" -C /var/lib/servercli control-plane
chmod 600 "$archive"
echo "control-plane backup: $archive"
exit 0
