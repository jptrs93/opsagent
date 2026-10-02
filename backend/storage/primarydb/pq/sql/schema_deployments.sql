-- Materialised views of the write log, maintained by the reducer in
-- pq/materialise.go. deployments holds the current row of every live
-- deployment; deployment_versions holds the current version of every live
-- deployment plus every version a retained scheduled instance pins, each
-- with the envelope of the write that produced it. A deleted deployment has
-- no current row; its pinned versions stay until the last pin goes.
CREATE TABLE IF NOT EXISTS deployments (
    id           INTEGER PRIMARY KEY CHECK (id BETWEEN 1 AND 16777215),
    space_id     INTEGER NOT NULL,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    spec_version INTEGER NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS deployment_versions (
    deployment_id INTEGER NOT NULL,
    version       INTEGER NOT NULL,
    spec_version  INTEGER NOT NULL,
    created_time  INTEGER NOT NULL,  -- epoch ms
    value         BLOB    NOT NULL,  -- Deployment without version, spec_version, created_time
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    PRIMARY KEY (deployment_id, version)
);
