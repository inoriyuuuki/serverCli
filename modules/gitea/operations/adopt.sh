#!/bin/sh
set -euo pipefail
# gitea adopt: take over an existing instance read-only: safety backup +
# marker. Adopted legacy instances keep their existing database (MariaDB
# 10.11 stays); adopt/repair/update never migrate implicitly.

if [ "$(id -u)" -ne 0 ]; then
  echo "gitea adopt: must run as root" >&2
  exit 1
fi
OWN_MARK="/var/lib/servercli/ownership/gitea.json"
if [ -f "$OWN_MARK" ]; then
  echo "gitea adopt: already adopted"
  exit 0
fi
DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/gitea}"
if [ ! -d "$DATA_DIR" ] || [ -z "$(find "$DATA_DIR" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ]; then
  echo "gitea adopt: nothing to adopt"
  exit 0
fi
mkdir -p /var/lib/servercli/backups/gitea /var/lib/servercli/ownership
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
tar -czf "/var/lib/servercli/backups/gitea/adopt-$stamp.tar.gz" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"
chmod 600 "/var/lib/servercli/backups/gitea/adopt-$stamp.tar.gz"
instance="$(hostname 2>/dev/null || echo localhost)"
backend="${SERVERCLI_CFG_BACKEND:-existing}"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"gitea","instance_id":"$instance","backend":"$backend","config_digest":"adopted-no-migration"}
MARK
chmod 600 "$OWN_MARK"
echo "gitea adopt: ok (backup + marker; existing database kept, no implicit migration)"
exit 0
