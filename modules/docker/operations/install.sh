#!/bin/sh
set -euo pipefail
# docker install: idempotent. Reuses a compatible existing installation and
# only creates ServerCLI-owned resources.

if [ "$(id -u)" -ne 0 ]; then
  echo "docker install: must run as root" >&2
  exit 1
fi

OWN_MARK="/var/lib/servercli/ownership/docker.json"
mkdir -p /var/lib/servercli/ownership

if command -v docker >/dev/null 2>&1; then
  version="$(docker --version 2>/dev/null | awk '{print $3}' | sed 's/,//' || true)"
  echo "docker install: reusing existing docker ${version:-unknown}"
else
  if command -v dnf >/dev/null 2>&1; then
    dnf -y install docker-ce docker-ce-cli containerd.io >/dev/null
  elif command -v yum >/dev/null 2>&1; then
    yum -y install docker-ce docker-ce-cli containerd.io >/dev/null
  else
    echo "docker install: no dnf/yum package manager found" >&2
    exit 1
  fi
  systemctl enable docker >/dev/null 2>&1 || true
  systemctl start docker >/dev/null 2>&1 || true
fi

# Wait for the daemon to become ready (retry loop; safe to rerun).
n=0
until docker info >/dev/null 2>&1; do
  n=$((n+1))
  if [ "$n" -ge 30 ]; then
    echo "docker install: daemon did not become ready" >&2
    exit 1
  fi
  sleep 2
done

instance="$(hostname 2>/dev/null || echo localhost)"
version="$(docker --version 2>/dev/null | awk '{print $3}' | sed 's/,//' || echo unknown)"
cat > "$OWN_MARK" <<MARK
{"managed_by":"servercli","module_id":"docker","instance_id":"$instance","version":"$version","config_digest":"pinned"}
MARK
chmod 600 "$OWN_MARK"
echo "docker install: ok"
exit 0
