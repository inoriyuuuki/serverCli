#!/bin/sh
set -euo pipefail
# v2ray verify: confirm config and ownership marker are in place.

is_enabled() {
  case "${SERVERCLI_CFG_ENABLED:-}" in
    ""|true|1|yes) return 0 ;;
    false|0|no) return 1 ;;
    *) return 0 ;;
  esac
}

if ! is_enabled; then
  echo "v2ray verify: disabled; ok"
  exit 0
fi

if [ ! -f /etc/servercli/proxy/v2ray.json ]; then
  echo "v2ray verify: config missing" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/v2ray.json ]; then
  echo "v2ray verify: ownership marker missing" >&2
  exit 1
fi
echo "v2ray verify: ok"
exit 0
