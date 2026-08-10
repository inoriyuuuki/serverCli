#!/bin/sh
# =============================================================================
# ServerCLI compatibility wrapper: restore
#
# Restore is a high-risk, explicit operation. There is no "restore latest"
# behaviour: you must name the service and the backup_id (or recovery_set_id).
#
# Usage:
#   restore.sh <service> <backup_id|recovery_set_id> [--yes]
#
#   --yes            skip the interactive confirmation (for scripts that have
#                    already been explicitly approved by an operator)
#   otherwise the command prompts on the TTY and requires the literal answer
#   "yes"
#
# Before restoring anything, servercli verifies the backup manifest signature
# and every file digest. Legacy backups (old format) are recognized read-only
# and are refused: they carry no signature/digests and can never be restored
# as a verified backup.
#
# Exit code is passed through unchanged. stdin/stdout/stderr and signals are
# passed through; the script never depends on the caller's cwd.
#
# Template variables (substituted by the installer):
#   {{SERVERCLI_BIN}}  absolute path to the servercli binary
# =============================================================================
set -u

if [ -z "${SERVERCLI_BIN:-}" ]; then
    SERVERCLI_BIN="{{SERVERCLI_BIN}}"
    case "${SERVERCLI_BIN}" in
        *'{{'*) SERVERCLI_BIN="/opt/servercli/bin/servercli" ;;
    esac
fi

if [ ! -x "${SERVERCLI_BIN}" ]; then
    echo "servercli: binary not found or not executable: ${SERVERCLI_BIN}" >&2
    exit 1
fi

if [ "$#" -lt 2 ]; then
    echo "usage: $0 <service> <backup_id|recovery_set_id> [--yes]" >&2
    exit 2
fi

exec "${SERVERCLI_BIN}" ops restore "$@"
