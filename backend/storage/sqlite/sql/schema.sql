-- Stable identity for a deployment. Created once, never mutated.
CREATE TABLE IF NOT EXISTS deployment_identifiers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    environment TEXT    NOT NULL,
    machine     TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    UNIQUE(environment, machine, name)
);

-- Current config + desired state for each deployment. One mutable row per deployment.
-- environment/machine/name are denormalized from deployment_identifiers so the
-- secondary can reuse this table without needing the identifiers table.
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);

-- Append-only log of every config mutation.
CREATE TABLE IF NOT EXISTS deployment_config_history (
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (deployment_id, version)
);

-- Current deployment status. One mutable row per deployment.
CREATE TABLE IF NOT EXISTS deployment_status (
    deployment_id           INTEGER PRIMARY KEY,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,  -- epoch ms
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER  -- epoch ms
);

-- Append-only log of status transitions reported by the operator.
CREATE TABLE IF NOT EXISTS deployment_status_history (
    deployment_id           INTEGER NOT NULL,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,  -- epoch ms
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    PRIMARY KEY (deployment_id, status_seq_no)
);

-- Append-only log of user config yaml submissions.
CREATE TABLE IF NOT EXISTS user_config_versions (
    version      INTEGER PRIMARY KEY,
    timestamp    INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    yaml_content TEXT    NOT NULL
);

-- Auth: passkey users.
CREATE TABLE IF NOT EXISTS users (
    id        INTEGER PRIMARY KEY,
    name      TEXT    NOT NULL,
    data_blob BLOB   NOT NULL
);

-- Auth: JWT signing keys.
CREATE TABLE IF NOT EXISTS public_keys (
    kid       TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL
);
