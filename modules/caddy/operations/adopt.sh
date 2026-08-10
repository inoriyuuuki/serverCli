#!/bin/sh
set -euo pipefail
# caddy adopt: take over an existing gateway read-only: safety backup + marker,
# config untouched. No implicit MariaDB/other migration happens here.

if [ "$(id -u)" -ne 0 ]; then
  echo "caddy adopt: must run as root" >&2
  exit 1
fi
OWN_MARK="/var/lib/servercli/ownership/caddy.json"
if [ -f "$OWN_MARK" ]; then
  echo "caddy adopt: already adopted"
  exit 0
fi
if [ ! -d /var/lib/servercli/caddy ] && [ ! -f /etc/caddy/Caddyfile ]; then
  echo "caddy adopt: nothing to adopt"
  exit 0
fi
mkdir -p /var/lib/servercli/backups/caddy /var/lib/servercli/ownership
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [ -d /var/lib/servercli/caddy ]; then
  tar -czf "/var/lib/servercli/backups/caddy/adopt-$stamp.tar.gz" -C /var/lib/servercli caddy
else
  tar -czf "/var/lib/servercli/backups/caddy/adopt-$stamp.tar.gz" -C /etc caddy/Caddyfile 2>/dev/null || true
fi
chmod 600 "/var/lib/servercli/backups/caddy/adopt-$stamp.tar.gz"
instance="$(hostname 2>/dev/null || echo localhost)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"caddy","instance_id":"$instance","config_digest":"adopted"}
MARK
chmod 600 "$OWN_MARK"
echo "caddy adopt: ok (backup + marker; config untouched)"
exit 0
