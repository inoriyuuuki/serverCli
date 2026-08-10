#!/bin/sh
set -euo pipefail
# gitea verify: container running + version API responds + marker present.

CONTAINER="servercli-gitea"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "gitea verify: container missing" >&2
  exit 1
fi
state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)"
if [ "$state" != "true" ]; then
  echo "gitea verify: container not running" >&2
  exit 1
fi
if ! docker exec "$CONTAINER" sh -c 'wget -qO- http://127.0.0.1:3000/api/v1/version >/dev/null 2>&1' 2>/dev/null; then
  echo "gitea verify: version API failed" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/gitea.json ]; then
  echo "gitea verify: ownership marker missing" >&2
  exit 1
fi
echo "gitea verify: ok"
exit 0
