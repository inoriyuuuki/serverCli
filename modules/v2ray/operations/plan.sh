#!/bin/sh
set -euo pipefail
# v2ray plan: print the intended changes without touching the system.

case "${SERVERCLI_CFG_ENABLED:-}" in
  false|0|no)
    echo "plan: v2ray disabled; no proxy install, direct connectivity will be verified"
    exit 0
    ;;
esac

mode="${SERVERCLI_CFG_MODE:-local}"
socks="${SERVERCLI_CFG_SOCKS_PORT:-1080}"
http="${SERVERCLI_CFG_HTTP_PORT:-8118}"
echo "plan: install v2ray proxy (mode=$mode socks=$socks http=$http)"
echo "plan: proxy domains: ${SERVERCLI_CFG_PROXY_DOMAINS:-<all>}"
echo "plan: direct domains: ${SERVERCLI_CFG_DIRECT_DOMAINS:-<none>}"
echo "plan: existing config without servercli ownership will block install"
exit 0
