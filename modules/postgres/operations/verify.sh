#!/bin/sh
set -euo pipefail
# postgres verify: pg_isready plus an actual app-account connection.

CONTAINER="servercli-postgres"
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "postgres verify: container missing" >&2
  exit 1
fi
if ! docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then
  echo "postgres verify: pg_isready failed" >&2
  exit 1
fi
APP_USER="${SERVERCLI_CFG_APP_USER:?APP_USER required}"
DB_NAME="${SERVERCLI_CFG_DB_NAME:?DB_NAME required}"
if ! docker exec "$CONTAINER" sh -c 'PGPASSWORD="$(cat /run/secrets/app)" exec psql -U '"$APP_USER"' -d '"$DB_NAME"' -Atqc "SELECT 1"' >/dev/null 2>&1; then
  echo "postgres verify: app connection failed" >&2
  exit 1
fi
echo "postgres verify: ok"
exit 0
