-- Global network override policies; see docs/future-work/network-policy-implementation-plan.md.
CREATE TABLE IF NOT EXISTS network_policy_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    created_time INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author       INTEGER NOT NULL,
    policy_id    INTEGER NOT NULL,
    version      INTEGER NOT NULL,  -- top-level: bumps on every event
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (policy_id, version)
);
