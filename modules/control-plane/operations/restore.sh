#!/bin/sh
set -euo pipefail
# control-plane restore: explicit backup id only.

if [ -z "${SERVERCLI_BACKUP_ID:-}" ]; then
  echo "control-plane restore: explicit BACKUP_ID required" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "control-plane restore: must run as root" >&2
  exit 1
fi
archive="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/control-plane}/$SERVERCLI_BACKUP_ID"
if [ ! -f "$archive" ]; then
  echo "control-plane restore: backup not found: $SERVERCLI_BACKUP_ID" >&2
  exit 1
fi
umask 077
tmp="$(mktemp -d /run/control-plane-restore.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$archive" -C "$tmp"
rm -rf /var/lib/servercli/control-plane
mv "$tmp/control-plane" /var/lib/servercli/control-plane
echo "control-plane restore: ok (restart control plane to apply)"
exit 0
