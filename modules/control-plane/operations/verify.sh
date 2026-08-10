#!/bin/sh
set -euo pipefail
# control-plane verify: container running and local health endpoint responds.

CONTAINER="servercli-control-plane"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "control-plane verify: container missing" >&2
  exit 1
fi
state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)"
if [ "$state" != "true" ]; then
  echo "control-plane verify: container not running" >&2
  exit 1
fi
if ! docker exec "$CONTAINER" sh -c 'wget -qO- http://127.0.0.1:'"${SERVERCLI_CFG_PORT:-8080}"'/healthz >/dev/null 2>&1' 2>/dev/null; then
  echo "control-plane verify: health endpoint failed" >&2
  exit 1
fi
echo "control-plane verify: ok"
exit 0
