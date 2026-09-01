-- Append-only deployment event log: the sole store of deployment intent.
-- Every event bumps the top-level version; sub-part version columns are a
-- queryable materialisation of the snapshot in value, which is the truth.
CREATE TABLE IF NOT EXISTS deployment_event_log (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq               INTEGER NOT NULL,
    created_at               INTEGER NOT NULL,  -- epoch ms
    author                   INTEGER NOT NULL,
    deployment_id            INTEGER NOT NULL CHECK (deployment_id BETWEEN 1 AND 16777215),
    version                  INTEGER NOT NULL,  -- top-level: bumps on every event
    spec_version             INTEGER NOT NULL,
    space_assignment_version INTEGER NOT NULL,
    name_version             INTEGER NOT NULL,
    value                    BLOB NOT NULL,     -- full Deployment snapshot
    event_type               INTEGER NOT NULL DEFAULT 0,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (deployment_id, version)
);

CREATE INDEX IF NOT EXISTS idx_deployment_event_log_spec_version
    ON deployment_event_log (deployment_id, spec_version);

-- The three tables below are the pre-event-log store. They are kept read-only
-- as the one-time migration source and will be dropped once every install has
-- rolled forward.
CREATE TABLE IF NOT EXISTS deployments (
    deployment_id   INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
    node_id         INTEGER NOT NULL DEFAULT -1,
    name            TEXT    NOT NULL DEFAULT '',
    deleted_at      INTEGER NOT NULL DEFAULT 0  -- epoch ms, 0 = not deleted
);

CREATE TABLE IF NOT EXISTS deployment_space_versions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id INTEGER NOT NULL,
    version       INTEGER NOT NULL,
    author        INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    space_id      INTEGER NOT NULL CHECK (space_id BETWEEN 0 AND 65535),
    global_seq    INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_space_versions_deployment_version
    ON deployment_space_versions (deployment_id, version);

CREATE TABLE IF NOT EXISTS deployment_spec_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    created_at      INTEGER NOT NULL,  -- epoch ms
    author          INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    global_seq      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (deployment_id, version)
);
