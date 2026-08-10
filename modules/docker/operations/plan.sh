#!/bin/sh
set -euo pipefail
# docker plan: print the intended install steps.

if command -v docker >/dev/null 2>&1; then
  echo "plan: reuse existing docker ($(docker --version 2>/dev/null || echo unknown))"
else
  echo "plan: install docker engine (version ${SERVERCLI_CFG_VERSION:-latest-supported})"
fi
echo "plan: enable and start docker daemon"
echo "plan: never clean non-ServerCLI containers/images/volumes"
exit 0
