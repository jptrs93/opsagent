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
