#!/bin/sh
set -euo pipefail
# postgres adopt: read-only discovery of an existing postgres data directory,
# safety backup, ownership marker. Never moves/deletes/rebuilds original data.

if [ "$(id -u)" -ne 0 ]; then
  echo "postgres adopt: must run as root" >&2
  exit 1
fi
OWN_MARK="/var/lib/servercli/ownership/postgres.json"
if [ -f "$OWN_MARK" ]; then
  echo "postgres adopt: already adopted"
  exit 0
fi
DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/postgres}"
if [ ! -d "$DATA_DIR" ] || [ -z "$(find "$DATA_DIR" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ]; then
  echo "postgres adopt: nothing to adopt"
  exit 0
fi
mkdir -p /var/lib/servercli/backups/postgres /var/lib/servercli/ownership
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
tar -czf "/var/lib/servercli/backups/postgres/adopt-$stamp.tar.gz" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"
chmod 600 "/var/lib/servercli/backups/postgres/adopt-$stamp.tar.gz"
instance="$(hostname 2>/dev/null || echo localhost)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"postgres","instance_id":"$instance","config_digest":"adopted"}
MARK
chmod 600 "$OWN_MARK"
echo "postgres adopt: ok (backup + marker; data untouched)"
exit 0
