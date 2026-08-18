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

-- Scheduled instances and status reports pin (deployment_id, version)
CREATE TABLE IF NOT EXISTS deployment_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    created_at      INTEGER NOT NULL,  -- epoch ms
    author      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    global_seq      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (deployment_id, version)
);
