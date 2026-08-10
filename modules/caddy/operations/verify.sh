#!/bin/sh
set -euo pipefail
# caddy verify: container running, config present, ownership marker present.

CONTAINER="servercli-caddy"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "caddy verify: container missing" >&2
  exit 1
fi
state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)"
if [ "$state" != "true" ]; then
  echo "caddy verify: container not running" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/caddy/config/Caddyfile ]; then
  echo "caddy verify: Caddyfile missing" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/caddy.json ]; then
  echo "caddy verify: ownership marker missing" >&2
  exit 1
fi
echo "caddy verify: ok"
exit 0
