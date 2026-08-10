#!/bin/sh
set -euo pipefail
# caddy restore: explicit backup id only; refuses when not servercli-owned.

if [ -z "${SERVERCLI_BACKUP_ID:-}" ]; then
  echo "caddy restore: explicit BACKUP_ID required" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "caddy restore: must run as root" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/caddy.json ]; then
  echo "caddy restore: no servercli ownership; refusing" >&2
  exit 1
fi
archive="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/caddy}/$SERVERCLI_BACKUP_ID"
if [ ! -f "$archive" ]; then
  echo "caddy restore: backup not found: $SERVERCLI_BACKUP_ID" >&2
  exit 1
fi
umask 077
tmp="$(mktemp -d /run/caddy-restore.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$archive" -C "$tmp"
rm -rf /var/lib/servercli/caddy
mv "$tmp/caddy" /var/lib/servercli/caddy
echo "caddy restore: ok (restart caddy to apply)"
exit 0
