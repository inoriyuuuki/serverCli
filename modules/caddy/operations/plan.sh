#!/bin/sh
set -euo pipefail
# caddy plan: two-phase gateway install; maintenance page hides internal state.

echo "plan: phase 1 maintenance mode (ACME TLS for ${SERVERCLI_CFG_DOMAIN:-<domain>})"
echo "plan: phase 2 formal route switch (Docker bridge + host-gateway)"
echo "plan: maintenance page does not expose internal state"
echo "plan: ACME failure degrades; postgres/docker/v2ray are preserved"
exit 0
