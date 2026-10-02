-- Materialised views of the write log, maintained by the reducer in
-- pq/materialise.go. scheduled_instances holds every non-final instance plus,
-- per ordinal of a live deployment with no non-final instance, its newest
-- final one; the row is rewritten on each state change and carries the
-- envelope of the last write.
CREATE TABLE IF NOT EXISTS scheduled_instances (
    id                      INTEGER PRIMARY KEY,
    deployment_id           INTEGER NOT NULL,
    deployment_version      INTEGER NOT NULL,  -- pinned deployment_versions row
    deployment_spec_version INTEGER NOT NULL,
    node_id                 INTEGER NOT NULL,
    instance_ordinal        INTEGER NOT NULL,
    space_id                INTEGER NOT NULL,
    state                   INTEGER NOT NULL,  -- ScheduledInstanceTarget
    created_time            INTEGER NOT NULL,  -- epoch ms
    seq                     INTEGER NOT NULL,
    event_time              INTEGER NOT NULL,  -- epoch ms
    author                  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_deployment_ordinal
    ON scheduled_instances (deployment_id, instance_ordinal, id);

-- The newest observed status per retained instance. updated_at is an HLC in
-- unix nanos; the reducer keeps the row with the greater clock and a row goes
-- with its instance. Earlier reports live in the log.
CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER PRIMARY KEY,
    updated_at              INTEGER NOT NULL,
    deployment_id           INTEGER NOT NULL,
    preparer_spec_version   INTEGER,
    preparer_artifact       TEXT,
    preparer_inputs_status  INTEGER NOT NULL DEFAULT 0,  -- stage 1: assets/secrets/configs
    preparer_image_status   INTEGER NOT NULL DEFAULT 0,  -- stage 2: build, pull, or download
    runner_spec_version     INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    runner_exit_code        INTEGER,
    seq                     INTEGER NOT NULL,
    event_time              INTEGER NOT NULL,  -- epoch ms
    author                  INTEGER NOT NULL,
    created_time            INTEGER NOT NULL   -- epoch ms of the first row
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instance_status_deployment_id
    ON scheduled_instance_status (deployment_id);
