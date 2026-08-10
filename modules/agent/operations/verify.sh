#!/bin/sh
set -euo pipefail
# agent verify: config and claim payload present with root-only permissions.

[ -f /etc/servercli/agent/config ] || { echo "agent verify: config missing" >&2; exit 1; }
[ -f /run/servercli/agent/claim.token ] || { echo "agent verify: claim payload missing" >&2; exit 1; }
[ -f /var/lib/servercli/ownership/agent.json ] || { echo "agent verify: ownership marker missing" >&2; exit 1; }
for f in /etc/servercli/agent/config /run/servercli/agent/claim.token /var/lib/servercli/ownership/agent.json; do
  mode="$(stat -c %a "$f" 2>/dev/null || stat -f %Lp "$f" 2>/dev/null || echo unknown)"
  case "$mode" in
    600|400|000) ;;
    *) echo "agent verify: $f not root-only ($mode)" >&2; exit 1 ;;
  esac
done
echo "agent verify: ok"
exit 0
