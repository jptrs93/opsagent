CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
    node_id         INTEGER NOT NULL DEFAULT -1,
    space_id        INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
    name            TEXT    NOT NULL DEFAULT '',
    deleted         INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_active_node_identity
    ON deployment_configs(node_id, space_id, name)
    WHERE deleted = 0;

-- Scheduled instances and status reports pin (deployment_id, version)
CREATE TABLE IF NOT EXISTS deployment_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    created_at      INTEGER NOT NULL,  -- epoch ms
    created_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    UNIQUE (deployment_id, version)
);

-- One row per placement/runtime incarnation. State lives entirely in the
-- scheduled_instance_versions log: current state is the newest row, creation
-- time the oldest. At most one serving incarnation may exist per identity
-- tuple below — with no state column this cannot be a SQL constraint, so it
-- is enforced solely by EnsureRunScheduledInstance under the service mutex.
CREATE TABLE IF NOT EXISTS scheduled_instances (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id      INTEGER NOT NULL,
    deployment_version INTEGER NOT NULL,
    node_id            INTEGER NOT NULL,
    instance_ordinal   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_deployment_ordinal
    ON scheduled_instances(deployment_id, instance_ordinal, id);

-- Append-only log of an incarnation's state transitions; version 1 is the
-- creation write. First/latest rows are identified by min/max id, and derived
-- state depends on them, so any future pruning must keep both endpoints of
-- each instance's log.
CREATE TABLE IF NOT EXISTS scheduled_instance_versions (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    scheduled_instance_id INTEGER NOT NULL,
    version               INTEGER NOT NULL,  -- derivable (count of prior rows + 1) but kept for convenience
    created_at            INTEGER NOT NULL,  -- epoch ms
    state                 INTEGER NOT NULL,  -- ScheduledInstanceTarget
    UNIQUE (scheduled_instance_id, version)
);

CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    deployment_id           INTEGER NOT NULL DEFAULT 0,
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_inputs_status  INTEGER NOT NULL DEFAULT 0,  -- stage 1: assets/secrets/configs
    preparer_image_status   INTEGER NOT NULL DEFAULT 0,  -- stage 2: build, pull, or download
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    PRIMARY KEY (scheduled_instance_id, updated_at)
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instance_status_deployment
    ON scheduled_instance_status(deployment_id, updated_at);
