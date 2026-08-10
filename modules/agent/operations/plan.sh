#!/bin/sh
set -euo pipefail
# agent plan: local bootstrap claim, then Caddy HTTPS, then heartbeat.

echo "plan: root-only local bootstrap claim channel"
echo "plan: claim binds environment/node-name/role/init transaction id"
echo "plan: claim token via /run 0600 file only (never argv/env/logs)"
echo "plan: switch to Caddy HTTPS after claim; heartbeat -> agent_ready/core_ready"
exit 0
