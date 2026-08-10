#!/bin/sh
# =============================================================================
# ServerCLI compatibility wrapper: backup
#
# Compatibility entry points preserved by this template:
#   /home/init/centos/backup.sh   (legacy host entry, kept working after adopt)
#   /opt/servercli/backup.sh      (current install entry)
#
# Semantics (identical to the legacy entry points):
#   - no arguments            -> back up ALL services
#   - named service arguments -> back up exactly those services
#   - a single failing service never stops the remaining services; servercli
#     aggregates the result and exits with the stable partial-success code
#   - exit code is passed through unchanged
#   - the script never depends on the caller's cwd (absolute paths only)
#   - stdin/stdout/stderr, signals and exit codes are passed through
#
# Template variables (substituted by the installer):
#   {{SERVERCLI_BIN}}  absolute path to the servercli binary
#   {{SERVICELIST}}    optional baked-in default service list (space separated)
#
# systemd timer semantics: backups are typically driven by a timer:
#   OnCalendar=*-*-* 02:30:00
#   ExecStart=/opt/servercli/backup.sh
# A backup never depends on the Control Plane being online; local backups
# always complete. Remote upload is handled by the configured uploader.
# =============================================================================
set -u

# The environment variable always wins. The template token is substituted by
# the installer; an unsubstituted "{{...}}" token falls back to the default.
if [ -z "${SERVERCLI_BIN:-}" ]; then
    SERVERCLI_BIN="{{SERVERCLI_BIN}}"
    case "${SERVERCLI_BIN}" in
        *'{{'*) SERVERCLI_BIN="/opt/servercli/bin/servercli" ;;
    esac
fi

DEFAULT_SERVICES="{{SERVICELIST}}"
case "${DEFAULT_SERVICES}" in
    *'{{'*) DEFAULT_SERVICES="" ;;
esac

if [ ! -x "${SERVERCLI_BIN}" ]; then
    echo "servercli: binary not found or not executable: ${SERVERCLI_BIN}" >&2
    exit 1
fi

if [ "$#" -gt 0 ]; then
    exec "${SERVERCLI_BIN}" ops backup "$@"
fi
if [ -n "${DEFAULT_SERVICES}" ]; then
    # shellcheck disable=SC2086
    exec "${SERVERCLI_BIN}" ops backup ${DEFAULT_SERVICES}
fi
exec "${SERVERCLI_BIN}" ops backup
