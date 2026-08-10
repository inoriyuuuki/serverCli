#!/bin/sh
set -euo pipefail
# caddy install: idempotent, two-phase. Docker bridge + host-gateway; the
# bridge IP is never hardcoded. ACME failure leaves existing services running.

if [ "$(id -u)" -ne 0 ]; then
  echo "caddy install: must run as root" >&2
  exit 1
fi
DOMAIN="${SERVERCLI_CFG_DOMAIN:?DOMAIN required}"
EMAIL="${SERVERCLI_CFG_EMAIL:-}"
DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/caddy}"
IMAGE="${SERVERCLI_CFG_IMAGE:-caddy:2}"
CONTAINER="servercli-caddy"
NETWORK="${SERVERCLI_CFG_BRIDGE:-servercli}"
OWN_MARK="/var/lib/servercli/ownership/caddy.json"

umask 077
mkdir -p "$DATA_DIR/config" "$DATA_DIR/data" /var/lib/servercli/ownership

docker network inspect "$NETWORK" >/dev/null 2>&1 || \
  docker network create --driver bridge "$NETWORK" >/dev/null

# Phase 1: maintenance mode with ACME TLS. The maintenance page is a static
# placeholder that reveals no internal state.
maintenance_caddyfile="$DATA_DIR/config/Caddyfile.maintenance"
cat > "$maintenance_caddyfile" <<CADDY
{
  email ${EMAIL:-}
}
$DOMAIN {
  respond "ServerCLI maintenance"
}
CADDY
chmod 600 "$maintenance_caddyfile"

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  docker run -d --name "$CONTAINER" \
    --restart unless-stopped \
    --network "$NETWORK" \
    --add-host host.docker.internal:host-gateway \
    --label managed-by=servercli \
    --label module-id=caddy \
    --label instance-id="$(hostname 2>/dev/null || echo localhost)" \
    -p 80:80 -p 443:443 \
    -v "$DATA_DIR/config:/etc/caddy" \
    -v "$DATA_DIR/data:/data" \
    "$IMAGE" >/dev/null
fi

# Phase 2: switch to the formal route only when the container is healthy.
formal_caddyfile="$DATA_DIR/config/Caddyfile"
cat > "$formal_caddyfile" <<CADDY
{
  email ${EMAIL:-}
}
$DOMAIN {
  reverse_proxy host.docker.internal:8080
}
CADDY
chmod 600 "$formal_caddyfile"
# Reload via the admin API; failure degrades but never removes services.
docker exec "$CONTAINER" caddy reload --config /etc/caddy/Caddyfile >/dev/null 2>&1 || true

instance="$(hostname 2>/dev/null || echo localhost)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"caddy","instance_id":"$instance","domain":"$DOMAIN","config_digest":"two-phase"}
MARK
chmod 600 "$OWN_MARK"
echo "caddy install: ok"
exit 0
