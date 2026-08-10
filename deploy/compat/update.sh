#!/bin/sh
# =============================================================================
# ServerCLI compatibility wrapper: update
#
# Compatibility entry points preserved by this template:
#   /home/init/centos/update.sh   (legacy host entry, kept working after adopt)
#   /opt/servercli/update.sh      (current install entry)
#
# Semantics (identical to the legacy entry points):
#   - no arguments            -> update ALL services
#   - named service arguments -> update exactly those services
#   - a single failing service never stops the remaining services; servercli
#     aggregates the result and exits with the stable partial-success code
#   - exit code is passed through unchanged (0 ok, 7 partial, 8 blocked, ...)
#   - the script never depends on the caller's cwd (absolute paths only)
#   - stdin/stdout/stderr, signals and exit codes are passed through
#
# Template variables (substituted by the installer):
#   {{SERVERCLI_BIN}}  absolute path to the servercli binary; the installer
#                      replaces this token with e.g. /opt/servercli/bin/servercli
#   {{SERVICELIST}}    optional baked-in default service list (space separated);
#                      when empty, "no arguments" means all services
#
# systemd timer semantics: run with Type=oneshot; no arguments means all
# services, so a timer such as
#   OnCalendar=*-*-* 03:00:00
#   ExecStart=/opt/servercli/update.sh
# keeps the legacy "update everything" behaviour.
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
    exec "${SERVERCLI_BIN}" ops update "$@"
fi
if [ -n "${DEFAULT_SERVICES}" ]; then
    # shellcheck disable=SC2086
    exec "${SERVERCLI_BIN}" ops update ${DEFAULT_SERVICES}
fi
exec "${SERVERCLI_BIN}" ops update
