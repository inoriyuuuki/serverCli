#!/bin/sh
set -euo pipefail
# control-plane install: idempotent container launch. Secrets flow only via
# container environment (single-line values); never argv or logs.

if [ "$(id -u)" -ne 0 ]; then
  echo "control-plane install: must run as root" >&2
  exit 1
fi

NODE_NAME="${SERVERCLI_CFG_NODE_NAME:?NODE_NAME required}"
ENVIRONMENT="${SERVERCLI_CFG_ENVIRONMENT:?ENVIRONMENT required}"
DB_NAME="${SERVERCLI_CFG_DB_NAME:?DB_NAME required}"
DB_USER="${SERVERCLI_CFG_DB_USER:?DB_USER required}"
DB_PW="${SERVERCLI_SEC_DB_PASSWORD:?DB_PASSWORD secret required}"
TOKEN="${SERVERCLI_SEC_INTERNAL_TOKEN:?INTERNAL_TOKEN secret required}"
IMAGE="${SERVERCLI_CFG_IMAGE:-servercli/control-plane:latest}"
DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/control-plane}"
PORT="${SERVERCLI_CFG_PORT:-8080}"
CONTAINER="servercli-control-plane"

umask 077
mkdir -p "$DATA_DIR" /var/lib/servercli/ownership

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  docker run -d --name "$CONTAINER" \
    --restart unless-stopped \
    --network servercli \
    --add-host host.docker.internal:host-gateway \
    --label managed-by=servercli \
    --label module-id=control-plane \
    --label instance-id="$(hostname 2>/dev/null || echo localhost)" \
    -e SERVERCLI_NODE_NAME="$NODE_NAME" \
    -e SERVERCLI_ENVIRONMENT="$ENVIRONMENT" \
    -e SERVERCLI_PG_DATABASE="$DB_NAME" \
    -e SERVERCLI_PG_USER="$DB_USER" \
    -e SERVERCLI_PG_PASSWORD="$DB_PW" \
    -e SERVERCLI_PG_HOST="${SERVERCLI_CFG_DB_HOST:-servercli-postgres}" \
    -e SERVERCLI_PG_PORT="${SERVERCLI_CFG_DB_PORT:-5432}" \
    -e SERVERCLI_INTERNAL_TOKEN="$TOKEN" \
    -v "$DATA_DIR:/var/lib/servercli" \
    "$IMAGE" >/dev/null
fi

# Health gate: wait for the local health endpoint via the bridge network.
n=0
until docker exec "$CONTAINER" sh -c \
     'wget -qO- http://127.0.0.1:'"$PORT"'/healthz >/dev/null 2>&1' 2>/dev/null; do
  n=$((n+1))
  if [ "$n" -ge 60 ]; then
    echo "control-plane install: health endpoint not ready" >&2
    exit 1
  fi
  sleep 2
done

instance="$(hostname 2>/dev/null || echo localhost)"
cat > /var/lib/servercli/ownership/control-plane.json <<MARK
{"managed_by":"servercli","module_id":"control-plane","instance_id":"$instance","node":"$NODE_NAME","config_digest":"ready"}
MARK
chmod 600 /var/lib/servercli/ownership/control-plane.json
echo "control-plane install: ok (control_plane_local_ready)"
exit 0
