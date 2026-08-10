#!/bin/sh
set -euo pipefail
# v2ray uninstall: removes only ServerCLI-owned proxy resources. Refuses when
# the marker is missing so foreign configuration is never touched.

if [ "$(id -u)" -ne 0 ]; then
  echo "v2ray uninstall: must run as root" >&2
  exit 1
fi
if [ ! -f /var/lib/servercli/ownership/v2ray.json ]; then
  echo "v2ray uninstall: no servercli ownership; refusing to remove foreign config" >&2
  exit 1
fi
rm -rf /etc/servercli/proxy
rm -f /var/lib/servercli/ownership/v2ray.json
echo "v2ray uninstall: ok"
exit 0
