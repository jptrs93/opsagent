-- Current config + desired state for each deployment. One row per deployment,
-- keyed by an integer id auto-allocated on first insert. (environment, machine,
-- name) is the human-readable identity and is unique; created_at is the
-- immutable first-seen time of that identity.
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0,  -- epoch ms; first-seen time of this deployment identity
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',  -- the desired version should uniquely identify that version and not be re-used over time
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_identity
    ON deployment_configs(environment, machine, name);

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
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds (0 = no status yet)
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
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    PRIMARY KEY (deployment_id, updated_at)
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
