#!/bin/sh
set -euo pipefail
# docker verify: daemon must be ready and the ownership marker present.

if ! command -v docker >/dev/null 2>&1; then
  echo "docker verify: docker missing" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "docker verify: daemon not ready" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/docker.json ]; then
  echo "docker verify: ownership marker missing" >&2
  exit 1
fi
echo "docker verify: ok"
exit 0
