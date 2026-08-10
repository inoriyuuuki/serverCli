#!/bin/sh
set -euo pipefail
# v2ray preflight: disabled modules need no local checks (the Go preflight
# probes direct connectivity instead). Enabled modules require an upstream
# target and free local ports.

is_enabled() {
  case "${SERVERCLI_CFG_ENABLED:-}" in
    ""|true|1|yes) return 0 ;;
    false|0|no) return 1 ;;
    *) return 0 ;;
  esac
}

if ! is_enabled; then
  echo "v2ray disabled by inventory; skipping local preflight"
  exit 0
fi

if [ -z "${SERVERCLI_CFG_UPSTREAM_HOST:-}" ]; then
  echo "v2ray preflight: enabled but UPSTREAM_HOST is empty" >&2
  exit 1
fi

for port in "${SERVERCLI_CFG_SOCKS_PORT:-}" "${SERVERCLI_CFG_HTTP_PORT:-}"; do
  [ -n "$port" ] || continue
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":$port$"; then
      echo "v2ray preflight: port $port already in use" >&2
      exit 1
    fi
  fi
done

echo "v2ray preflight: ok"
exit 0
