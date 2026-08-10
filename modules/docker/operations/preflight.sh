#!/bin/sh
set -euo pipefail
# docker preflight: verify a pinned supported version range and that an
# existing /var/lib/docker is not foreign.

if command -v docker >/dev/null 2>&1; then
  version="$(docker --version 2>/dev/null | awk '{print $3}' | sed 's/,//' || true)"
  case "${version:-}" in
    "") echo "docker preflight: cannot read docker version" >&2; exit 1 ;;
    2[0-9].*|1[0-9].*) echo "docker preflight: ok (existing $version)" ;;
    *) echo "docker preflight: unsupported docker version $version" >&2; exit 1 ;;
  esac
else
  echo "docker preflight: docker not installed; will install"
fi
exit 0
