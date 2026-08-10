#!/bin/sh
set -euo pipefail
# postgres plan: pinned image, dedicated db, least-privilege account.

echo "plan: run ${SERVERCLI_CFG_IMAGE:-paradedb/paradedb:pg17} (digest ${SERVERCLI_CFG_IMAGE_DIGEST:-<pinned-by-bundle>})"
echo "plan: data dir ${SERVERCLI_CFG_DATA_DIR:-/var/lib/servercli/postgres}"
echo "plan: create database ${SERVERCLI_CFG_DB_NAME:-<db>} and user ${SERVERCLI_CFG_APP_USER:-<user>}"
echo "plan: existing database is never rebuilt or reset"
echo "plan: no automatic restore by default"
exit 0
