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
    author      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    UNIQUE (deployment_id, version)
);
