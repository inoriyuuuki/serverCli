#!/bin/sh
set -euo pipefail
# agent preflight: required binding fields must be present (delivery=file, so
# env values are paths to 0600 files under /run).

for key in NODE_NAME ENVIRONMENT ROLE TRANSACTION_ID; do
  eval "v=\${SERVERCLI_CFG_$key:-}"
  if [ -z "$v" ]; then
    echo "agent preflight: $key missing" >&2
    exit 1
  fi
done
socket_dir="${SERVERCLI_CFG_SOCKET_DIR:-/run/servercli/agent}"
if [ -e "$socket_dir" ] && [ ! -d "$socket_dir" ]; then
  echo "agent preflight: socket dir exists and is not a directory" >&2
  exit 1
fi
echo "agent preflight: ok"
exit 0
