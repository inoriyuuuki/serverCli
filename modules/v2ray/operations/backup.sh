#!/bin/sh
set -euo pipefail
# v2ray backup: archive config (never secrets; config holds no secret values).

is_enabled() {
  case "${SERVERCLI_CFG_ENABLED:-}" in
    ""|true|1|yes) return 0 ;;
    false|0|no) return 1 ;;
    *) return 0 ;;
  esac
}

if ! is_enabled; then
  echo "v2ray backup: disabled; nothing to back up"
  exit 0
fi
if [ ! -d /etc/servercli/proxy ]; then
  echo "v2ray backup: nothing to back up"
  exit 0
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "v2ray backup: must run as root" >&2
  exit 1
fi

backup_dir="${SERVERCLI_BACKUP_DIR:-/var/lib/servercli/backups/v2ray}"
mkdir -p "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$backup_dir/${SERVERCLI_BACKUP_ID:-v2ray-$stamp}.tar.gz"
tar -czf "$archive" -C /etc/servercli proxy
chmod 600 "$archive"
echo "v2ray backup: $archive"
exit 0
