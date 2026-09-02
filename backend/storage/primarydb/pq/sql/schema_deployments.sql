CREATE TABLE IF NOT EXISTS deployment_event_log (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq               INTEGER NOT NULL,
    event_time               INTEGER NOT NULL,  -- epoch ms
    created_time             INTEGER NOT NULL,  -- epoch ms
    author                   INTEGER NOT NULL,
    deployment_id            INTEGER NOT NULL CHECK (deployment_id BETWEEN 1 AND 16777215),
    version                  INTEGER NOT NULL,  -- top-level: bumps on every event
    spec_version             INTEGER NOT NULL,
    space_assignment_version INTEGER NOT NULL,
    name_version             INTEGER NOT NULL,
    spec_changed             INTEGER NOT NULL DEFAULT 0,  -- 1 iff this event bumped spec_version
    space_assignment_changed INTEGER NOT NULL DEFAULT 0,  -- 1 iff this event bumped space_assignment_version
    name_changed             INTEGER NOT NULL DEFAULT 0,  -- 1 iff this event bumped name_version
    value                    BLOB NOT NULL,     -- DeploymentDef snapshot
    event_type               INTEGER NOT NULL DEFAULT 0,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (deployment_id, version)
);

-- Spec-writing rows only: exactly one per (deployment_id, spec_version).
CREATE INDEX IF NOT EXISTS idx_deployment_event_log_spec_version
    ON deployment_event_log (deployment_id, spec_version)
    WHERE spec_changed != 0;
