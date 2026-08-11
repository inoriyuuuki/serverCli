-- 0008: Declarative multi-node ops V2 entities.
-- Shared between SQLite and PostgreSQL (TEXT/INTEGER only, RFC3339 UTC TEXT timestamps).

CREATE TABLE cluster (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    environment             TEXT NOT NULL,
    active_primary_node_id  TEXT,
    primary_epoch           INTEGER NOT NULL DEFAULT 0,
    release_channel         TEXT,
    oss_provider_ref        TEXT,
    update_policy_json      TEXT,
    backup_policy_json      TEXT,
    status                  TEXT NOT NULL DEFAULT 'active',
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);
CREATE INDEX idx_cluster_env ON cluster(environment);

CREATE TABLE node_profile (
    id                       TEXT PRIMARY KEY,
    cluster_id               TEXT NOT NULL,
    name                     TEXT NOT NULL,
    version                  TEXT NOT NULL DEFAULT '1',
    modules_json             TEXT,
    default_config_json      TEXT,
    secret_refs_json         TEXT,
    backup_policy_json       TEXT,
    update_policy_json       TEXT,
    verification_policy_json TEXT,
    labels_json              TEXT,
    resources_json           TEXT,
    status                   TEXT NOT NULL DEFAULT 'active',
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    UNIQUE(cluster_id, name, version)
);
CREATE INDEX idx_node_profile_cluster ON node_profile(cluster_id);

CREATE TABLE declarative_node (
    id                   TEXT PRIMARY KEY,
    cluster_id           TEXT NOT NULL,
    node_id              TEXT NOT NULL,
    role                 TEXT NOT NULL DEFAULT 'child',
    profile_id           TEXT,
    lifecycle            TEXT NOT NULL DEFAULT 'draft',
    status               TEXT NOT NULL DEFAULT 'pending',
    labels_json          TEXT,
    addresses_json       TEXT,
    os_name              TEXT,
    os_version           TEXT,
    arch                 TEXT,
    desired_revision     TEXT,
    applied_revision     TEXT,
    identity_generation  INTEGER NOT NULL DEFAULT 0,
    replacement_status   TEXT,
    agent_status         TEXT,
    legacy_mac           TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    retired_at           TEXT,
    UNIQUE(cluster_id, node_id)
);
CREATE INDEX idx_declarative_node_cluster ON declarative_node(cluster_id);
CREATE INDEX idx_declarative_node_profile ON declarative_node(profile_id);

CREATE TABLE service_reference (
    id                  TEXT PRIMARY KEY,
    cluster_id          TEXT NOT NULL,
    name                TEXT NOT NULL,
    service_instance_id TEXT,
    node_id             TEXT,
    address             TEXT,
    port                INTEGER NOT NULL DEFAULT 0,
    secret_ref_json     TEXT,
    status              TEXT NOT NULL DEFAULT 'active',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE(cluster_id, name)
);
CREATE INDEX idx_service_reference_cluster ON service_reference(cluster_id);

CREATE TABLE desired_state_revision (
    id         TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    revision   INTEGER NOT NULL,
    profile_id TEXT,
    node_id    TEXT,
    state_json TEXT,
    checksum   TEXT,
    source     TEXT,
    status     TEXT NOT NULL DEFAULT 'planned',
    created_at TEXT NOT NULL,
    UNIQUE(cluster_id, revision)
);
CREATE INDEX idx_desired_state_cluster ON desired_state_revision(cluster_id);

CREATE TABLE applied_state_revision (
    id         TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    node_id    TEXT NOT NULL,
    revision_id TEXT,
    checksum   TEXT,
    status     TEXT NOT NULL DEFAULT 'applied',
    result_json TEXT,
    applied_at TEXT NOT NULL
);
CREATE INDEX idx_applied_state_node ON applied_state_revision(node_id);

CREATE TABLE operation_v2 (
    id                  TEXT PRIMARY KEY,
    operation_id        TEXT NOT NULL,
    operation_type      TEXT NOT NULL,
    cluster_id          TEXT,
    node_id             TEXT,
    module_id           TEXT,
    service_instance_id TEXT,
    desired_revision    TEXT,
    arguments_json      TEXT,
    approval            TEXT,
    risk_level          TEXT,
    idempotency_key     TEXT,
    deadline            TEXT,
    primary_epoch       INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'planned',
    requested_by        TEXT,
    created_at          TEXT NOT NULL,
    started_at          TEXT,
    finished_at         TEXT,
    error_code          TEXT,
    error_message       TEXT,
    UNIQUE(idempotency_key)
);
CREATE INDEX idx_operation_v2_status ON operation_v2(status);
CREATE INDEX idx_operation_v2_node ON operation_v2(node_id);
CREATE INDEX idx_operation_v2_cluster ON operation_v2(cluster_id);

CREATE TABLE operation_step (
    id           TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operation_v2(id) ON DELETE CASCADE,
    sequence     INTEGER NOT NULL,
    module_id    TEXT,
    operation    TEXT,
    attempt      INTEGER NOT NULL DEFAULT 0,
    commit_point TEXT,
    status       TEXT NOT NULL DEFAULT 'pending',
    error_type   TEXT,
    message      TEXT,
    started_at   TEXT,
    completed_at TEXT,
    UNIQUE(operation_id, sequence)
);
CREATE INDEX idx_operation_step_op ON operation_step(operation_id);

CREATE TABLE backup_set (
    id                  TEXT PRIMARY KEY,
    backup_id           TEXT NOT NULL,
    recovery_set_id     TEXT,
    cluster_id          TEXT,
    node_id             TEXT,
    service_instance_id TEXT,
    module_version      TEXT,
    app_version         TEXT,
    schema_version      TEXT,
    files_json          TEXT,
    sha256              TEXT,
    size_bytes          INTEGER NOT NULL DEFAULT 0,
    oss_key             TEXT,
    status              TEXT NOT NULL DEFAULT 'verified',
    created_at          TEXT NOT NULL
);
CREATE INDEX idx_backup_set_node ON backup_set(node_id);
CREATE INDEX idx_backup_set_status ON backup_set(status);

CREATE TABLE oss_sync_revision (
    id          TEXT PRIMARY KEY,
    cluster_id  TEXT,
    kind        TEXT NOT NULL,
    object_key  TEXT NOT NULL,
    sha256      TEXT,
    direction   TEXT NOT NULL DEFAULT 'upload',
    status      TEXT NOT NULL DEFAULT 'uploaded',
    etag        TEXT,
    created_at  TEXT NOT NULL,
    verified_at TEXT
);
CREATE INDEX idx_oss_sync_kind ON oss_sync_revision(kind);

CREATE TABLE release_cache_entry (
    id                TEXT PRIMARY KEY,
    version           TEXT NOT NULL,
    source_repository TEXT,
    source_release    TEXT,
    os                TEXT,
    arch              TEXT,
    artifact_name     TEXT NOT NULL,
    artifact_size     INTEGER NOT NULL DEFAULT 0,
    sha256            TEXT NOT NULL,
    modules_version   TEXT,
    schema_min        TEXT,
    schema_max        TEXT,
    oss_key           TEXT,
    status            TEXT NOT NULL DEFAULT 'pending',
    uploaded_at       TEXT,
    verified_at       TEXT,
    created_at        TEXT NOT NULL,
    UNIQUE(version, artifact_name)
);
CREATE INDEX idx_release_cache_version ON release_cache_entry(version);
CREATE INDEX idx_release_cache_status ON release_cache_entry(status);

CREATE TABLE primary_transfer (
    id            TEXT PRIMARY KEY,
    cluster_id    TEXT NOT NULL,
    from_node_id  TEXT NOT NULL,
    to_node_id    TEXT NOT NULL,
    primary_epoch INTEGER NOT NULL,
    status        TEXT NOT NULL DEFAULT 'transfer_planning',
    backup_set_id TEXT,
    steps_json    TEXT,
    error_code    TEXT,
    error_message TEXT,
    requested_by  TEXT,
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    completed_at  TEXT
);
CREATE INDEX idx_primary_transfer_cluster ON primary_transfer(cluster_id);
CREATE INDEX idx_primary_transfer_status ON primary_transfer(status);
