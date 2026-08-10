#!/bin/sh
set -euo pipefail
# docker adopt: verify an existing installation is compatible and record
# ownership without modifying or cleaning anything.

if [ "$(id -u)" -ne 0 ]; then
  echo "docker adopt: must run as root" >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "docker adopt: no working docker to adopt" >&2
  exit 1
fi

OWN_MARK="/var/lib/servercli/ownership/docker.json"
if [ -f "$OWN_MARK" ]; then
  echo "docker adopt: already adopted"
  exit 0
fi
mkdir -p /var/lib/servercli/ownership
instance="$(hostname 2>/dev/null || echo localhost)"
version="$(docker --version 2>/dev/null | awk '{print $3}' | sed 's/,//' || echo unknown)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"docker","instance_id":"$instance","version":"$version","config_digest":"adopted"}
MARK
chmod 600 "$OWN_MARK"
echo "docker adopt: ok"
exit 0
