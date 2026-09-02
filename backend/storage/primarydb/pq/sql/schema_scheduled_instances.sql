-- Append-only event log of scheduled instance incarnations, one row per state
-- transition; version 1 is the creation write. Identity columns are immutable
-- per instance and denormalised onto every row; created_time is the first
-- event's event_time copied to every row. Current state is the highest-version
-- row. At most one serving incarnation may exist per identity tuple — with
-- state living only in the log this cannot be a SQL constraint, so it is
-- enforced solely by EnsureRunScheduledInstance under the service mutex.
-- Instance ids are allocated as MAX(scheduled_instance_id)+1 under the same
-- mutex; the log is never pruned, so ids are never reused.
CREATE TABLE IF NOT EXISTS scheduled_instance_event_log (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq              INTEGER NOT NULL,
    event_time              INTEGER NOT NULL,  -- epoch ms
    created_time            INTEGER NOT NULL,  -- epoch ms, first event's event_time
    scheduled_instance_id   INTEGER NOT NULL,
    version                 INTEGER NOT NULL,  -- per-instance: bumps on every event
    deployment_id           INTEGER NOT NULL,
    deployment_version      INTEGER NOT NULL,  -- pinned deployment_event_log version
    deployment_spec_version INTEGER NOT NULL,  -- denormalised from the pinned version's row
    node_id                 INTEGER NOT NULL,
    instance_ordinal        INTEGER NOT NULL,
    space_id                INTEGER NOT NULL,  -- denormalised from the pinned version's row
    state                   INTEGER NOT NULL,  -- ScheduledInstanceTarget
    UNIQUE (scheduled_instance_id, version)
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instance_event_log_deployment_ordinal
    ON scheduled_instance_event_log (deployment_id, instance_ordinal, scheduled_instance_id);

CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    deployment_id           INTEGER NOT NULL DEFAULT 0,
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
    PRIMARY KEY (scheduled_instance_id, updated_at)
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instance_status_deployment
    ON scheduled_instance_status(deployment_id, updated_at);
