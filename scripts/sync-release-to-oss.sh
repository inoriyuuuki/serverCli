#!/usr/bin/env bash
# Restricted wrapper for servercli-release-sync.
#
# Credential contract (values are never placed in argv or printed):
#   - GitHub: export GITHUB_TOKEN=...
#   - OSS env: export OSS_ACCESS_KEY_ID=... OSS_ACCESS_KEY_SECRET=...
#   - OSS file: export OSS_AK_FILE=/run/secrets/servercli-oss (must be mode 0600)
#
# Examples:
#   scripts/sync-release-to-oss.sh plan --owner acme --repo servercli --tag v1.2.3
#   OSS_AK_FILE=/run/secrets/servercli-oss scripts/sync-release-to-oss.sh apply \
#     --owner acme --repo servercli --tag v1.2.3 \
#     --oss-endpoint https://oss-cn-hangzhou.aliyuncs.com --oss-bucket releases

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${SERVERCLI_RELEASE_SYNC_BIN:-${REPO_ROOT}/backend/servercli-release-sync}"

usage() {
  sed -n '2,15p' "$0" >&2
}

[ "$#" -gt 0 ] || { usage; exit 2; }

# Refuse credential-bearing flags even if the binary later gains one. A path to
# a protected credential file is allowed; the file content remains off argv.
for arg in "$@"; do
  case "${arg,,}" in
    --*secret*|--*password*|--*token*|--*access-key-id*|--*access-key-secret*)
      echo "sync-release-to-oss.sh: credential flags are forbidden; use environment variables or OSS_AK_FILE" >&2
      exit 2
      ;;
  esac
done

[ -x "$BIN" ] || {
  echo "sync-release-to-oss.sh: binary not found or not executable: $BIN" >&2
  echo "build it with: (cd ${REPO_ROOT}/backend && go build -o servercli-release-sync ./cmd/servercli-release-sync)" >&2
  exit 1
}

extra_args=()
if [ "$1" = "apply" ] && [ -n "${OSS_AK_FILE:-}" ]; then
  [ -f "$OSS_AK_FILE" ] || { echo "sync-release-to-oss.sh: OSS_AK_FILE is not a regular file" >&2; exit 1; }
  mode="$(stat -f '%Lp' "$OSS_AK_FILE" 2>/dev/null || stat -c '%a' "$OSS_AK_FILE")"
  [ "$mode" = "600" ] || { echo "sync-release-to-oss.sh: OSS_AK_FILE must have mode 0600" >&2; exit 1; }
  extra_args+=(--oss-ak-file "$OSS_AK_FILE")
fi

exec "$BIN" "$@" "${extra_args[@]}"
