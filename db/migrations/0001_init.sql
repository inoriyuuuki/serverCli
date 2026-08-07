-- ServerCLI initial schema.
-- Written in a dialect-neutral subset (TEXT/INTEGER/REAL) so the same file
-- runs on SQLite and PostgreSQL. Booleans are stored as INTEGER 0/1 and
-- timestamps as TEXT RFC3339 UTC, normalized by the data access layer.

CREATE TABLE admin_user (
    id                   TEXT PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    password_changed_at  TEXT,
    failed_login_count   INTEGER NOT NULL DEFAULT 0,
    locked_until         TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE admin_session (
    id               TEXT PRIMARY KEY,
    admin_user_id    TEXT NOT NULL REFERENCES admin_user(id) ON DELETE CASCADE,
    token_hash       TEXT NOT NULL,
    csrf_secret_hash TEXT NOT NULL,
    ip_address       TEXT,
    user_agent       TEXT,
    expires_at       TEXT NOT NULL,
    revoked_at       TEXT,
    last_seen_at     TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX idx_admin_session_token ON admin_session(token_hash);
CREATE INDEX idx_admin_session_user ON admin_session(admin_user_id);

CREATE TABLE node_enrollment (
    id                      TEXT PRIMARY KEY,
    instance_request_id     TEXT NOT NULL,
    environment_id          TEXT NOT NULL,
    requested_role          TEXT NOT NULL,
    hostname                TEXT NOT NULL,
    source_ip               TEXT,
    reported_addresses_json TEXT,
    agent_version           TEXT,
    os_name                 TEXT,
    os_version              TEXT,
    arch                    TEXT,
    frontend_port           INTEGER NOT NULL DEFAULT 0,
    backend_port            INTEGER NOT NULL DEFAULT 0,
    status                  TEXT NOT NULL DEFAULT 'pending',
    reviewed_by             TEXT,
    reviewed_at             TEXT,
    review_note             TEXT,
    claim_token_hash        TEXT,
    claim_expires_at        TEXT,
    claimed_at              TEXT,
    instance_public_key     TEXT,
    node_id                 TEXT,
    created_at              TEXT NOT NULL,
    UNIQUE(environment_id, instance_request_id)
);
CREATE INDEX idx_enrollment_status ON node_enrollment(status);

CREATE TABLE node (
    id                 TEXT PRIMARY KEY,
    environment_id     TEXT NOT NULL,
    instance_name      TEXT NOT NULL,
    alias              TEXT,
    role               TEXT NOT NULL,
    hostname           TEXT,
    status             TEXT NOT NULL DEFAULT 'pending',
    enabled            INTEGER NOT NULL DEFAULT 1,
    agent_version      TEXT,
    app_version        TEXT,
    os_name            TEXT,
    os_version         TEXT,
    arch               TEXT,
    frontend_port      INTEGER NOT NULL DEFAULT 0,
    backend_port       INTEGER NOT NULL DEFAULT 0,
    last_heartbeat_at  TEXT,
    last_online_at     TEXT,
    labels_json        TEXT,
    metadata_json      TEXT,
    credential_hash    TEXT,
    credential_prefix  TEXT,
    credential_version INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX idx_node_environment ON node(environment_id);
CREATE INDEX idx_node_status ON node(status);

CREATE TABLE node_address (
    id            TEXT PRIMARY KEY,
    node_id       TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    address       TEXT NOT NULL,
    address_type  TEXT NOT NULL,
    service_port  INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    is_preferred  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_node_address_node ON node_address(node_id);
CREATE INDEX idx_node_address_addr ON node_address(address);

CREATE TABLE node_heartbeat (
    id                  TEXT PRIMARY KEY,
    node_id             TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    recorded_at         TEXT NOT NULL,
    cpu_usage_percent   REAL NOT NULL DEFAULT 0,
    memory_total_bytes  INTEGER NOT NULL DEFAULT 0,
    memory_used_bytes   INTEGER NOT NULL DEFAULT 0,
    disk_total_bytes    INTEGER NOT NULL DEFAULT 0,
    disk_used_bytes     INTEGER NOT NULL DEFAULT 0,
    load_1              REAL NOT NULL DEFAULT 0,
    load_5              REAL NOT NULL DEFAULT 0,
    load_15             REAL NOT NULL DEFAULT 0,
    uptime_seconds      INTEGER NOT NULL DEFAULT 0,
    time_offset_ms      INTEGER NOT NULL DEFAULT 0,
    summary_json        TEXT,
    is_protected        INTEGER NOT NULL DEFAULT 0,
    protected_at        TEXT
);
CREATE INDEX idx_heartbeat_node_time ON node_heartbeat(node_id, recorded_at);

CREATE TABLE node_command (
    id                    TEXT PRIMARY KEY,
    node_id               TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    command_id            TEXT NOT NULL,
    command_version       TEXT NOT NULL,
    capability_id         TEXT,
    category              TEXT,
    title                 TEXT,
    description           TEXT,
    parameter_schema_json TEXT,
    permission_profile    TEXT NOT NULL DEFAULT 'read-only',
    timeout_seconds       INTEGER NOT NULL DEFAULT 60,
    max_output_bytes      INTEGER NOT NULL DEFAULT 262144,
    enabled               INTEGER NOT NULL DEFAULT 1,
    manifest_hash         TEXT,
    executable_hash       TEXT,
    first_seen_at         TEXT NOT NULL,
    last_seen_at          TEXT NOT NULL,
    UNIQUE(node_id, command_id, command_version)
);
CREATE INDEX idx_node_command_node ON node_command(node_id);

CREATE TABLE task (
    id                 TEXT PRIMARY KEY,
    node_id            TEXT NOT NULL REFERENCES node(id),
    command_id         TEXT NOT NULL,
    command_version    TEXT NOT NULL,
    requested_by       TEXT NOT NULL,
    idempotency_key    TEXT NOT NULL,
    arguments_json     TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'queued',
    queued_at          TEXT NOT NULL,
    started_at         TEXT,
    finished_at        TEXT,
    timeout_seconds    INTEGER NOT NULL DEFAULT 60,
    exit_code          INTEGER,
    error_code         TEXT,
    error_message      TEXT,
    result_summary_json TEXT,
    is_protected       INTEGER NOT NULL DEFAULT 0,
    protected_at       TEXT,
    UNIQUE(requested_by, idempotency_key)
);
CREATE INDEX idx_task_node ON task(node_id);
CREATE INDEX idx_task_status ON task(status);

CREATE TABLE task_event (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL,
    event_type  TEXT NOT NULL,
    status      TEXT,
    message     TEXT,
    occurred_at TEXT NOT NULL,
    source      TEXT,
    UNIQUE(task_id, sequence)
);
CREATE INDEX idx_task_event_task ON task_event(task_id);

CREATE TABLE task_output (
    task_id         TEXT PRIMARY KEY REFERENCES task(id) ON DELETE CASCADE,
    stdout_text     TEXT NOT NULL DEFAULT '',
    stderr_text     TEXT NOT NULL DEFAULT '',
    stdout_bytes    INTEGER NOT NULL DEFAULT 0,
    stderr_bytes    INTEGER NOT NULL DEFAULT 0,
    truncated       INTEGER NOT NULL DEFAULT 0,
    redaction_count INTEGER NOT NULL DEFAULT 0,
    encoding        TEXT NOT NULL DEFAULT 'utf-8',
    created_at      TEXT NOT NULL,
    is_protected    INTEGER NOT NULL DEFAULT 0,
    protected_at    TEXT
);

CREATE TABLE ai_lease_request (
    id                        TEXT PRIMARY KEY,
    client_request_id         TEXT NOT NULL,
    environment_id            TEXT NOT NULL,
    ai_agent_id               TEXT,
    ai_agent_name             TEXT,
    node_id                   TEXT NOT NULL,
    requested_profile         TEXT NOT NULL,
    requested_duration_seconds INTEGER NOT NULL DEFAULT 3600,
    public_key                TEXT NOT NULL,
    public_key_fingerprint    TEXT,
    purpose                   TEXT,
    status                    TEXT NOT NULL DEFAULT 'pending',
    decision_reason           TEXT,
    source_ip                 TEXT,
    client_metadata_json      TEXT,
    created_at                TEXT NOT NULL,
    decided_at                TEXT,
    is_protected              INTEGER NOT NULL DEFAULT 0,
    protected_at              TEXT,
    UNIQUE(environment_id, client_request_id)
);
CREATE INDEX idx_lease_request_node ON ai_lease_request(node_id);
CREATE INDEX idx_lease_request_status ON ai_lease_request(status);

CREATE TABLE ai_lease (
    id                     TEXT PRIMARY KEY,
    request_id             TEXT NOT NULL REFERENCES ai_lease_request(id),
    node_id                TEXT NOT NULL,
    ai_agent_id            TEXT,
    permission_profile     TEXT NOT NULL,
    public_key             TEXT NOT NULL,
    public_key_fingerprint TEXT,
    issued_at              TEXT NOT NULL,
    expires_at             TEXT NOT NULL,
    absolute_expires_at    TEXT NOT NULL,
    last_renewed_at        TEXT,
    renew_count            INTEGER NOT NULL DEFAULT 0,
    status                 TEXT NOT NULL DEFAULT 'active',
    revoked_at             TEXT,
    revoke_reason          TEXT,
    renewal_disabled       INTEGER NOT NULL DEFAULT 0,
    renewal_token_hash     TEXT NOT NULL,
    renewal_token_prefix   TEXT NOT NULL,
    active_session_count   INTEGER NOT NULL DEFAULT 0,
    last_heartbeat_at      TEXT,
    key_installed          INTEGER NOT NULL DEFAULT 0,
    key_installed_at       TEXT,
    is_protected           INTEGER NOT NULL DEFAULT 0,
    protected_at           TEXT
);
CREATE INDEX idx_ai_lease_node ON ai_lease(node_id);
CREATE INDEX idx_ai_lease_status ON ai_lease(status);
CREATE INDEX idx_ai_lease_expires ON ai_lease(expires_at);

CREATE TABLE ai_lease_event (
    id          TEXT PRIMARY KEY,
    lease_id    TEXT NOT NULL REFERENCES ai_lease(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    actor_type  TEXT,
    actor_id    TEXT,
    details_json TEXT,
    occurred_at TEXT NOT NULL
);
CREATE INDEX idx_ai_lease_event_lease ON ai_lease_event(lease_id);

CREATE TABLE ai_ssh_session (
    id            TEXT PRIMARY KEY,
    lease_id      TEXT NOT NULL REFERENCES ai_lease(id) ON DELETE CASCADE,
    node_id       TEXT NOT NULL,
    remote_address TEXT,
    connection_id TEXT,
    os_pid        INTEGER,
    cgroup_id     TEXT,
    started_at    TEXT NOT NULL,
    last_seen_at  TEXT,
    ended_at      TEXT,
    end_reason    TEXT,
    exit_code     INTEGER,
    command_count INTEGER NOT NULL DEFAULT 0,
    recording_ref TEXT,
    is_protected  INTEGER NOT NULL DEFAULT 0,
    protected_at  TEXT
);
CREATE INDEX idx_ssh_session_lease ON ai_ssh_session(lease_id);

CREATE TABLE audit_event (
    id             TEXT PRIMARY KEY,
    occurred_at    TEXT NOT NULL,
    environment_id TEXT,
    node_id        TEXT,
    actor_type     TEXT NOT NULL,
    actor_id       TEXT,
    action         TEXT NOT NULL,
    resource_type  TEXT,
    resource_id    TEXT,
    result         TEXT NOT NULL,
    request_id     TEXT,
    task_id        TEXT,
    lease_id       TEXT,
    session_id     TEXT,
    source_ip      TEXT,
    summary        TEXT,
    details_json   TEXT,
    risk_level     TEXT NOT NULL DEFAULT 'low',
    is_protected   INTEGER NOT NULL DEFAULT 0,
    protected_at   TEXT,
    protected_by   TEXT
);
CREATE INDEX idx_audit_occurred ON audit_event(occurred_at);
CREATE INDEX idx_audit_node ON audit_event(node_id);
CREATE INDEX idx_audit_action ON audit_event(action);
CREATE INDEX idx_audit_resource ON audit_event(resource_type, resource_id);

CREATE TABLE system_setting (
    key        TEXT PRIMARY KEY,
    value      TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE cleanup_run (
    id                     TEXT PRIMARY KEY,
    started_at             TEXT NOT NULL,
    finished_at            TEXT,
    trigger_type           TEXT NOT NULL,
    policy_snapshot_json   TEXT,
    candidate_count        INTEGER NOT NULL DEFAULT 0,
    deleted_count          INTEGER NOT NULL DEFAULT 0,
    skipped_protected_count INTEGER NOT NULL DEFAULT 0,
    status                 TEXT NOT NULL,
    error_message          TEXT,
    requested_by           TEXT,
    is_protected           INTEGER NOT NULL DEFAULT 0,
    protected_at           TEXT
);
