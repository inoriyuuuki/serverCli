#!/bin/sh
set -euo pipefail
# v2ray adopt: take over an existing proxy configuration read-only. Creates a
# safety backup and an ownership marker; never rewrites the existing config.

if [ "$(id -u)" -ne 0 ]; then
  echo "v2ray adopt: must run as root" >&2
  exit 1
fi

OWN_MARK="/var/lib/servercli/ownership/v2ray.json"
if [ -f "$OWN_MARK" ]; then
  echo "v2ray adopt: already adopted"
  exit 0
fi
if [ ! -f /etc/servercli/proxy/v2ray.json ]; then
  echo "v2ray adopt: nothing to adopt (no existing config)"
  exit 0
fi

mkdir -p /var/lib/servercli/backups/v2ray /var/lib/servercli/ownership
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
tar -czf "/var/lib/servercli/backups/v2ray/adopt-$stamp.tar.gz" -C /etc/servercli proxy
chmod 600 "/var/lib/servercli/backups/v2ray/adopt-$stamp.tar.gz"
instance="$(hostname 2>/dev/null || echo localhost)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"v2ray","instance_id":"$instance","config_digest":"adopted"}
MARK
chmod 600 "$OWN_MARK"
echo "v2ray adopt: ok (backup + marker, config untouched)"
exit 0
