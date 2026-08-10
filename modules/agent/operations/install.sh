#!/bin/sh
set -euo pipefail
# agent install: root-only config + single-use claim payload. The claim token
# is read from the runner-provided 0600 file and stored into a root-only
# bootstrap claim file; it is never echoed, and never placed in argv or env.

if [ "$(id -u)" -ne 0 ]; then
  echo "agent install: must run as root" >&2
  exit 1
fi

# delivery=file: env values are paths to 0600 files under /run.
NODE_NAME="$(cat "${SERVERCLI_CFG_NODE_NAME:?NODE_NAME missing}")"
ENVIRONMENT="$(cat "${SERVERCLI_CFG_ENVIRONMENT:?ENVIRONMENT missing}")"
ROLE="$(cat "${SERVERCLI_CFG_ROLE:?ROLE missing}")"
TX_ID="$(cat "${SERVERCLI_CFG_TRANSACTION_ID:?TRANSACTION_ID missing}")"
CP_ADDRESS="${SERVERCLI_CFG_CP_ADDRESS:-}"

CLAIM_TOKEN_FILE="${SERVERCLI_SEC_CLAIM_TOKEN:?CLAIM_TOKEN missing}"
if [ ! -f "$CLAIM_TOKEN_FILE" ]; then
  echo "agent install: claim token file missing" >&2
  exit 1
fi
# Permissions of the runner-provided secret file must be root-only.
mode="$(stat -c %a "$CLAIM_TOKEN_FILE" 2>/dev/null || stat -f %Lp "$CLAIM_TOKEN_FILE" 2>/dev/null || echo unknown)"
case "$mode" in
  600|400|000) ;;
  *) echo "agent install: claim token file permissions not root-only ($mode)" >&2; exit 1 ;;
esac

umask 077
mkdir -p /etc/servercli/agent /run/servercli/agent /var/lib/servercli/ownership

# Root-only config (non-secret binding fields).
cat > /etc/servercli/agent/config <<CONF
NODE_NAME=$NODE_NAME
ENVIRONMENT=$ENVIRONMENT
ROLE=$ROLE
TRANSACTION_ID=$TX_ID
CP_ADDRESS=${CP_ADDRESS:-https://caddy.gateway.invalid}
CONF
chmod 600 /etc/servercli/agent/config

# Single-use claim payload: copy the token into a root-only claim file. The
# original runner file is removed by the runner itself after the operation.
cp "$CLAIM_TOKEN_FILE" /run/servercli/agent/claim.token
chmod 600 /run/servercli/agent/claim.token

instance="$(hostname 2>/dev/null || echo localhost)"
cat > /var/lib/servercli/ownership/agent.json <<MARK
{"managed_by":"servercli","module_id":"agent","instance_id":"$instance","node":"$NODE_NAME","config_digest":"claim-ready"}
MARK
chmod 600 /var/lib/servercli/ownership/agent.json
echo "agent install: ok (claim payload staged, token not exposed)"
exit 0
