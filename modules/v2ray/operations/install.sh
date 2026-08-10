#!/bin/sh
set -euo pipefail
# v2ray install: idempotent. Never overwrites existing proxy configuration
# that ServerCLI does not own. Secret values are never echoed.

if [ "$(id -u)" -ne 0 ]; then
  echo "v2ray install: must run as root" >&2
  exit 1
fi

is_enabled() {
  case "${SERVERCLI_CFG_ENABLED:-}" in
    ""|true|1|yes) return 0 ;;
    false|0|no) return 1 ;;
    *) return 0 ;;
  esac
}

if ! is_enabled; then
  echo "v2ray disabled by inventory; nothing to install"
  exit 0
fi

OWN_MARK="/var/lib/servercli/ownership/v2ray.json"
CFG_DIR="/etc/servercli/proxy"
CFG_FILE="$CFG_DIR/v2ray.json"
ENV_FILE="$CFG_DIR/env"

if [ -f "$CFG_FILE" ] && [ ! -f "$OWN_MARK" ]; then
  echo "v2ray install: existing proxy config without servercli ownership; refusing to overwrite" >&2
  exit 1
fi

umask 077
mkdir -p "$CFG_DIR" /var/lib/servercli/ownership

upstream_host="${SERVERCLI_CFG_UPSTREAM_HOST:-}"
upstream_port="${SERVERCLI_CFG_UPSTREAM_PORT:-443}"
socks_port="${SERVERCLI_CFG_SOCKS_PORT:-1080}"
http_port="${SERVERCLI_CFG_HTTP_PORT:-8118}"

# Write config atomically (tmp + mv). Placeholder upstream only; real values
# arrive via inventory config fields and never contain secrets.
tmp="$(mktemp "$CFG_DIR/.v2ray.json.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" <<CONF
{
  "inbounds": [
    {"protocol": "socks", "port": $socks_port},
    {"protocol": "http", "port": $http_port}
  ],
  "outbounds": [
    {"protocol": "vmess", "settings": {"vnext": [{"address": "$upstream_host", "port": $upstream_port}]}}
  ]
}
CONF
mv "$tmp" "$CFG_FILE"
chmod 600 "$CFG_FILE"

# Proxy environment for curl/git/docker daemon/module downloader. Single-line,
# non-secret values only; never the upstream credentials.
cat > "$ENV_FILE" <<ENV
http_proxy=http://127.0.0.1:$http_port
https_proxy=http://127.0.0.1:$http_port
all_proxy=socks5://127.0.0.1:$socks_port
no_proxy=127.0.0.1,localhost,${SERVERCLI_CFG_DIRECT_DOMAINS:-}
ENV
chmod 600 "$ENV_FILE"

# Ownership marker: managed-by/module-id/instance-id/config digest.
digest="$(awk '{s+=length($0)} END {print s}' "$CFG_FILE" 2>/dev/null || echo unknown)"
instance="$(hostname 2>/dev/null || echo localhost)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"v2ray","instance_id":"$instance","config_digest":"$digest"}
MARK
chmod 600 "$OWN_MARK"

echo "v2ray install: ok (config written, marker created)"
exit 0
