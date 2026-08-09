-- ServerCLI: AI device-node auto-approval rules + task parameter history.
-- Written in the same dialect-neutral subset (TEXT/INTEGER/REAL) so the file
-- runs on SQLite and PostgreSQL. Booleans are INTEGER 0/1, timestamps TEXT
-- RFC3339 UTC. No FK constraints are declared here: node cascade deletion is
-- handled explicitly by the data access layer.

CREATE TABLE ai_auto_approval (
    id                TEXT PRIMARY KEY,
    environment_id    TEXT NOT NULL,
    ai_agent_id       TEXT NOT NULL,
    ai_agent_name     TEXT,
    node_id           TEXT NOT NULL,
    source_request_id TEXT,
    created_by        TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    expires_at        TEXT NOT NULL,
    UNIQUE(environment_id, ai_agent_id, node_id)
);
CREATE INDEX idx_ai_auto_approval_node ON ai_auto_approval(node_id);
CREATE INDEX idx_ai_auto_approval_expires ON ai_auto_approval(expires_at);

CREATE TABLE task_parameter_history (
    id                  TEXT PRIMARY KEY,
    node_id             TEXT NOT NULL,
    command_id          TEXT NOT NULL,
    command_version     TEXT NOT NULL,
    arguments_json      TEXT NOT NULL,
    arguments_hash      TEXT NOT NULL,
    last_task_id        TEXT,
    first_used_at       TEXT NOT NULL,
    last_used_at        TEXT NOT NULL,
    use_count           INTEGER NOT NULL DEFAULT 1,
    UNIQUE(node_id, command_id, command_version, arguments_hash)
);
CREATE INDEX idx_task_parameter_history_node_cmd
    ON task_parameter_history(node_id, command_id, command_version);

-- A lease request can issue at most one lease; the unique index is a second
-- line of defense (after the guarded pending update) against concurrent
-- double-approval of the same request.
CREATE UNIQUE INDEX idx_ai_lease_request_unique ON ai_lease(request_id);
