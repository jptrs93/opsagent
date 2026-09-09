CREATE TABLE IF NOT EXISTS node_event_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq     INTEGER NOT NULL,
    event_time     INTEGER NOT NULL,  -- epoch ms
    created_time   INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author         INTEGER NOT NULL,
    node_id        INTEGER NOT NULL,
    version        INTEGER NOT NULL,  -- top-level: bumps on every event
    name           TEXT    NOT NULL,
    identifier     TEXT    NOT NULL,
    enrolled_time  INTEGER NOT NULL,  -- 0 until accept; stamped by the accept event, copied forward
    status         INTEGER NOT NULL,
    roles          TEXT    NOT NULL,  -- JSON
    addresses      TEXT    NOT NULL,  -- JSON
    wg_public_key  TEXT    NOT NULL,
    allowed_spaces TEXT    NOT NULL,  -- JSON
    event_type     INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    host_addresses TEXT NOT NULL DEFAULT '[]',
    enrollment_requested_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (node_id, version)
);

-- Observed history is retained in full. updated_at is an HLC in unix nanos.
CREATE TABLE IF NOT EXISTS node_status_log (
    node_id INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    global_seq INTEGER NOT NULL DEFAULT 0,
    last_connected_at INTEGER NOT NULL DEFAULT 0,
    is_connected INTEGER NOT NULL DEFAULT 0,
    opendeploy_version TEXT NOT NULL DEFAULT '',
    remote_address TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, updated_at)
);
