#!/bin/sh
set -euo pipefail
# control-plane preflight: postgres must be ready and caddy gateway running.

if ! docker inspect servercli-postgres >/dev/null 2>&1 || \
   ! docker exec servercli-postgres pg_isready -U postgres >/dev/null 2>&1; then
  echo "control-plane preflight: postgres not ready" >&2
  exit 1
fi
if ! docker inspect servercli-caddy >/dev/null 2>&1; then
  echo "control-plane preflight: caddy gateway not running" >&2
  exit 1
fi
echo "control-plane preflight: ok"
exit 0
