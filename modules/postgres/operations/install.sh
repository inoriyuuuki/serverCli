#!/bin/sh
set -euo pipefail
# postgres install: idempotent. Never rebuilds/resets an existing database.
# Secrets are delivered via a 0600 env-file to `docker run` and a read-only
# in-container secrets mount for `docker exec`; values never reach argv or logs.

if [ "$(id -u)" -ne 0 ]; then
  echo "postgres install: must run as root" >&2
  exit 1
fi

DATA_DIR="${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/postgres}"
DB_NAME="${SERVERCLI_CFG_DB_NAME:?DB_NAME required}"
APP_USER="${SERVERCLI_CFG_APP_USER:?APP_USER required}"
APP_PW="${SERVERCLI_SEC_APP_PASSWORD:?APP_PASSWORD secret required}"
SUPER_PW="${SERVERCLI_SEC_POSTGRES_SUPER_PASSWORD:?POSTGRES_SUPER_PASSWORD secret required}"
IMAGE="${SERVERCLI_CFG_IMAGE:-paradedb/paradedb:pg17}"
if [ -n "${SERVERCLI_CFG_IMAGE_DIGEST:-}" ]; then
  IMAGE="$IMAGE@sha256:${SERVERCLI_CFG_IMAGE_DIGEST}"
fi
PORT="${SERVERCLI_CFG_PORT:-5432}"
CONTAINER="servercli-postgres"
SECRETS_DIR="$DATA_DIR/secrets"

# Block on a foreign (non-empty, unowned) data directory.
if [ -d "$DATA_DIR" ] && [ -n "$(find "$DATA_DIR" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ] \
   && [ ! -f /var/lib/servercli/ownership/postgres.json ]; then
  echo "postgres install: data dir exists without servercli ownership; blocked" >&2
  exit 1
fi

umask 077
mkdir -p "$DATA_DIR" /var/lib/servercli/ownership "$SECRETS_DIR"
chmod 700 "$DATA_DIR" "$SECRETS_DIR"

# Persist the passwords in the host secrets dir (0600) mounted ro into the
# container so docker exec never passes them via argv.
printf '%s' "$SUPER_PW" > "$SECRETS_DIR/super"
printf '%s' "$APP_PW" > "$SECRETS_DIR/app"
chmod 600 "$SECRETS_DIR/super" "$SECRETS_DIR/app"

# Reuse an existing container; never rebuild or reset it.
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  envf="$(mktemp /run/servercli-pg.env.XXXXXX)"
  trap 'rm -f "$envf"' EXIT
  chmod 600 "$envf"
  printf 'POSTGRES_PASSWORD=%s\n' "$SUPER_PW" > "$envf"
  docker run -d --name "$CONTAINER" \
    --restart unless-stopped \
    --label managed-by=servercli \
    --label module-id=postgres \
    --label instance-id="$(hostname 2>/dev/null || echo localhost)" \
    --env-file "$envf" \
    -e PGDATA=/var/lib/postgresql/data/pgdata \
    -v "$DATA_DIR:/var/lib/postgresql/data" \
    -v "$SECRETS_DIR:/run/secrets:ro" \
    -p 127.0.0.1:"$PORT":5432 \
    "$IMAGE" >/dev/null
  rm -f "$envf"
  trap - EXIT
fi

# Wait for readiness.
n=0
until docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1; do
  n=$((n+1))
  if [ "$n" -ge 60 ]; then
    echo "postgres install: pg_isready timed out" >&2
    exit 1
  fi
  sleep 2
done

# Create the dedicated database if missing.
if ! docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '"$DB_NAME"'"' | grep -q 1; then
  docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"'"$DB_NAME"'\""' >/dev/null
fi

# Create the least-privilege role if missing.
if ! docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -Atqc "SELECT 1 FROM pg_roles WHERE rolname = '"$APP_USER"'"' | grep -q 1; then
  docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE ROLE \"'"$APP_USER"'\" LOGIN"' >/dev/null
fi

# Set password + grants via a 0600 SQL file under /run; the secret never
# reaches argv or logs and the file is removed on exit.
sql="$(mktemp /run/servercli-pg.sql.XXXXXX)"
trap 'rm -f "$sql"' EXIT
chmod 600 "$sql"
cat > "$sql" <<SQL
ALTER ROLE "$APP_USER" WITH PASSWORD '$APP_PW';
GRANT CONNECT ON DATABASE "$DB_NAME" TO "$APP_USER";
GRANT USAGE ON SCHEMA public TO "$APP_USER";
SQL
docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/super)" exec psql -U postgres -v ON_ERROR_STOP=1 -q' < "$sql" >/dev/null

# Actual connection test with the app account before continuing.
if ! docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/app)" exec psql -U '"$APP_USER"' -d '"$DB_NAME"' -Atqc "SELECT 1"' >/dev/null 2>&1; then
  echo "postgres install: app account connection check failed" >&2
  exit 1
fi

instance="$(hostname 2>/dev/null || echo localhost)"
cat > /var/lib/servercli/ownership/postgres.json <<MARK
{"managed_by":"servercli","module_id":"postgres","instance_id":"$instance","db":"$DB_NAME","config_digest":"pinned-image"}
MARK
chmod 600 /var/lib/servercli/ownership/postgres.json

echo "postgres install: ok (database ready)"
exit 0
