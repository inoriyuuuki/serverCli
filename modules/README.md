# ServerCLI Foundation Modules

This directory holds the declarative module manifests and fixed operation
entrypoints executed by the ServerCLI Provision Runner (`servercli modules run`).
Each subdirectory is one module:

| module          | phase            | role                                                                   |
|-----------------|------------------|------------------------------------------------------------------------|
| `v2ray`         | foundation-core  | Optional outbound proxy (inventory-controlled). When disabled, direct connectivity to GitHub/OSS/mirror is verified. Never overwrites unowned proxy config. |
| `docker`        | foundation-core  | Pinned Docker Engine; reuses an existing compatible install. Never cleans non-ServerCLI containers/images/volumes. |
| `postgres`      | foundation-core  | Pinned `paradedb/paradedb:pg17` with data under `/var/lib/servercli/postgres`; dedicated DB + least-privilege account; never rebuilds/resets; blocked on unowned non-empty data dir; no automatic restore. |
| `caddy`         | foundation-core  | Gateway on a Docker bridge with host-gateway (no hardcoded bridge IP); two-phase install (maintenance ACME TLS -> formal routes); maintenance page hides internal state; ACME failure degrades without removing services. |
| `control-plane` | foundation-core  | Starts after postgres + caddy are ready; production PostgreSQL required; reachable via Caddy bridge; health gate records `control_plane_local_ready`. |
| `agent`         | foundation-core  | Root-only agent with a local bootstrap claim channel; claim token via `/run` 0600 file only (never argv/env/logs); switches to Caddy HTTPS after claim; heartbeat records `agent_ready`/`core_ready`. |
| `gitea`         | foundation-core  | Part of foundation but NOT part of the `core_ready` hard gate (`core_ready` = v2ray,docker,postgres,caddy,control-plane,agent; `foundation_complete` = core_ready + gitea). Fresh installs use a dedicated Foundation PostgreSQL DB; adopted legacy instances keep MariaDB 10.11 with no implicit migration. |

Execution order: `v2ray -> docker -> postgres -> caddy -> control-plane -> agent
-> gitea` (see `backend/internal/modules/registry.go`).

## module.yaml contract

Every `module.yaml` must satisfy `modman.ModuleManifest.Validate`:

- `id` / `version` / `phase` (one of `foundation-core`, `foundation-services`, `services`)
- `depends_on` only references known module ids
- `delivery` is `env` (single-line values via `SERVERCLI_CFG_*` / `SERVERCLI_SEC_*`)
  or `file` (`SERVERCLI_*` point at 0600 files under `/run`; used for claim
  tokens, certs, private keys and multi-line content)
- `config_fields` / `secret_fields` with `name`/`type`/`required`/`sensitive`
- `operations` keys restricted to `install`/`preflight`/`plan`/`verify`/`backup`/`restore`/`adopt`/`uninstall`;
  `entry` is a module-relative path (`operations/*.sh`)
- `healthcheck` / `backup` / `concurrency` as declared

## Operation script rules

- POSIX `sh` with `set -euo pipefail`; idempotent and retry-safe.
- Read config/secret only from `SERVERCLI_CFG_*` / `SERVERCLI_SEC_*` environment
  variables, or from the `/run` 0600 files those variables point to
  (`delivery=file` modules).
- Never echo, log, or argv-pass secret values. Secrets that must reach another
  process (e.g. SQL password) go through a short-lived 0600 file under `/run`
  that is removed on exit.

## Managed resources and ownership

- Every resource created by a module is tagged or marked:
  - containers: `managed-by=servercli`, `module-id=<id>`, `instance-id=<hostname>`, `config digest`
  - filesystem state: a root-only marker under `/var/lib/servercli/ownership/<module>.json`
    with `managed_by`/`module_id`/`instance_id`/`config_digest`
  - backups: `/var/lib/servercli/backups/<module>/<id>` with generated ids
- **Do not clean resources ServerCLI does not own.** Modules refuse to
  overwrite/remove/restore foreign state: unowned proxy config, unowned
  non-empty data directories (blocked, use `adopt`), or non-ServerCLI
  containers/images/volumes.
- Restore always requires an explicit backup id; never automatic.
