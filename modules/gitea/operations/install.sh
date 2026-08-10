#!/bin/sh
set -euo pipefail
# gitea install: idempotent. Fresh installs use a dedicated Foundation
# PostgreSQL database. Adopted legacy instances are handled by adopt.sh and
# never implicitly migrated.

if [ "$(id -u)" -ne 0 ]; then
  echo "gitea install: must run as root" >&2
  exit 1
fi

DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/gitea}"
IMAGE="${SERVERCLI_CFG_IMAGE:-gitea/gitea:1.22}"
PORT="${SERVERCLI_CFG_PORT:-3000}"
DOMAIN="${SERVERCLI_CFG_DOMAIN:-}"
CONTAINER="servercli-gitea"
BACKEND="${SERVERCLI_CFG_BACKEND:-postgres}"

# Foreign data dir -> blocked (must adopt).
if [ -d "$DATA_DIR" ] && [ -n "$(find "$DATA_DIR" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ] \
   && [ ! -f /var/lib/servercli/ownership/gitea.json ]; then
  echo "gitea install: data dir exists without servercli ownership; blocked" >&2
  exit 1
fi

umask 077
mkdir -p "$DATA_DIR" /var/lib/servercli/ownership

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  envf="$(mktemp /run/servercli-gitea.env.XXXXXX)"
  trap 'rm -f "$envf"' EXIT
  chmod 600 "$envf"
  if [ -n "${SERVERCLI_SEC_DB_PASSWORD:-}" ]; then
    printf 'GITEA__database__PASSWD=%s\n' "${SERVERCLI_SEC_DB_PASSWORD}" > "$envf"
  fi
  docker run -d --name "$CONTAINER" \
    --restart unless-stopped \
    --network servercli \
    --label managed-by=servercli \
    --label module-id=gitea \
    --label instance-id="$(hostname 2>/dev/null || echo localhost)" \
    -e GITEA__server__DOMAIN="$DOMAIN" \
    -e GITEA__server__ROOT_URL="https://$DOMAIN/" \
    -e GITEA__database__DB_TYPE=postgres \
    -e GITEA__database__HOST="${SERVERCLI_CFG_DB_HOST:-servercli-postgres}:${SERVERCLI_CFG_DB_PORT:-5432}" \
    -e GITEA__database__NAME="${SERVERCLI_CFG_DB_NAME:-gitea}" \
    -e GITEA__database__USER="${SERVERCLI_CFG_DB_USER:-gitea}" \
    --env-file "$envf" \
    -v "$DATA_DIR:/data" \
    -p 127.0.0.1:"$PORT":3000 \
    "$IMAGE" >/dev/null
fi

# Wait for gitea to be ready (retry loop).
n=0
until docker exec "$CONTAINER" sh -c \
     'wget -qO- http://127.0.0.1:3000/api/v1/version >/dev/null 2>&1' 2>/dev/null; do
  n=$((n+1))
  if [ "$n" -ge 60 ]; then
    echo "gitea install: gitea did not become ready" >&2
    exit 1
  fi
  sleep 2
done

instance="$(hostname 2>/dev/null || echo localhost)"
cat > /var/lib/servercli/ownership/gitea.json <<MARK
{"managed_by":"servercli","module_id":"gitea","instance_id":"$instance","backend":"$BACKEND","config_digest":"ready"}
MARK
chmod 600 /var/lib/servercli/ownership/gitea.json
echo "gitea install: ok"
exit 0
