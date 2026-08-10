#!/bin/sh
set -euo pipefail
# postgres preflight: block when a non-empty data directory has no ServerCLI
# ownership metadata; require docker.

if [ ! -d /var/lib/servercli/postgres ]; then
  echo "postgres preflight: data dir absent; fresh install path"
  exit 0
fi
if [ -n "$(find /var/lib/servercli/postgres -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ] \
   && [ ! -f /var/lib/servercli/ownership/postgres.json ]; then
  echo "postgres preflight: data dir exists without servercli ownership; blocked" >&2
  exit 1
fi
echo "postgres preflight: ok"
exit 0
