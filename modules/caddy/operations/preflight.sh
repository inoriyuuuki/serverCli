#!/bin/sh
set -euo pipefail
# caddy preflight: ports 80/443 must be free (or owned by servercli caddy),
# docker must be available, and the domain must be set.

if [ -z "${SERVERCLI_CFG_DOMAIN:-}" ]; then
  echo "caddy preflight: DOMAIN required" >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "caddy preflight: docker not ready" >&2
  exit 1
fi
if command -v ss >/dev/null 2>&1; then
  for port in 80 443; do
    if ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":$port$"; then
      if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'servercli-caddy'; then
        echo "caddy preflight: port $port in use by a foreign process" >&2
        exit 1
      fi
    fi
  done
fi
echo "caddy preflight: ok"
exit 0
