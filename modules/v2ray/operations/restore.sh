#!/bin/sh
set -euo pipefail
# v2ray restore: explicit backup id only; refuses to overwrite config that
# ServerCLI does not own.

if [ -z "${SERVERCLI_BACKUP_ID:-}" ]; then
  echo "v2ray restore: explicit BACKUP_ID required" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/v2ray.json ]; then
  echo "v2ray restore: no servercli ownership; refusing restore" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "v2ray restore: must run as root" >&2
  exit 1
fi

archive="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/v2ray}/$SERVERCLI_BACKUP_ID"
if [ ! -f "$archive" ]; then
  echo "v2ray restore: backup not found: $SERVERCLI_BACKUP_ID" >&2
  exit 1
fi
umask 077
tmp="$(mktemp -d /run/v2ray-restore.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$archive" -C "$tmp"
rm -rf /etc/servercli/proxy
mv "$tmp/proxy" /etc/servercli/proxy
echo "v2ray restore: ok"
exit 0
