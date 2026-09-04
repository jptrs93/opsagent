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
    UNIQUE (node_id, version)
);

CREATE TABLE IF NOT EXISTS node_statuses (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id            INTEGER NOT NULL,
    last_connected_at  INTEGER NOT NULL DEFAULT 0,
    is_connected       INTEGER NOT NULL DEFAULT 0,
    opendeploy_version TEXT    NOT NULL DEFAULT '',
    remote_address     TEXT    NOT NULL DEFAULT '',
    enrollment_pending INTEGER NOT NULL DEFAULT 0,
    host_addresses     TEXT    NOT NULL DEFAULT '[]',  -- JSON, last reported by the node
    UNIQUE(node_id)
);
