-- Stable deployment identities, mirroring the secrets/configs identity +
-- versions split. One row per deployment, ever: deletion is a tombstone (the
-- partial unique index below frees the identity tuple for reuse), and the node
-- is fixed for the life of a deployment. The spec lives in immutable
-- deployment_versions rows; creation/update times and attribution are
-- derived from the first/latest version row. space_id is identity-level state:
-- moving spaces does not mint a version.
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

-- Immutable spec versions, append-only, mirroring config_versions. The current
-- desired state is the MAX(version) row; scheduled instances and status reports
-- pin (deployment_id, version) pairs, so rows must never be renumbered or
-- pruned. Deleting a deployment appends a final workload-stopped version, so a
-- tombstone's deletion time is its latest version's created_at. id is a stable
-- no-reuse identity for the version row itself; nothing pins it yet.
CREATE TABLE IF NOT EXISTS deployment_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,  -- version could be implicit but is kept to make pruning possible.
    created_at      INTEGER NOT NULL,  -- epoch ms
    created_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    UNIQUE (deployment_id, version)
);

-- One row per placement/runtime incarnation. state: 0=run, 1=terminate, 2=finalized.
CREATE TABLE IF NOT EXISTS scheduled_instances (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at         INTEGER NOT NULL,  -- epoch ms
    deployment_id      INTEGER NOT NULL,
    deployment_version INTEGER NOT NULL,
    node_id            INTEGER NOT NULL,
    instance_ordinal   INTEGER NOT NULL DEFAULT 0,
    state              INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_deployment_ordinal
    ON scheduled_instances(deployment_id, instance_ordinal, id);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_node_active
    ON scheduled_instances(node_id, state);

CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_instances_unique_run
    ON scheduled_instances(deployment_id, deployment_version, node_id, instance_ordinal)
    WHERE state = 0;

-- Append-only observed status for each scheduled instance. Latest row per
-- scheduled_instance_id is the current status.
CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    deployment_id           INTEGER NOT NULL DEFAULT 0,
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    -- The two preparation stages. There is no stored rollup: it is derived from
    -- this pair on read, so the two cannot disagree with it. Not nullable, since
    -- zero already means UNKNOWN and presence is decided by
    -- preparer_config_version.
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

-- The primary key covers per-instance lookups. Deployment history spans every
-- instance of a deployment, and this table is append-only and unbounded, so
-- that query needs its own index to avoid a full scan that grows over time.
CREATE INDEX IF NOT EXISTS idx_scheduled_instance_status_deployment
    ON scheduled_instance_status(deployment_id, updated_at);

