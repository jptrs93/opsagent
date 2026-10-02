-- Materialised views of the write log, maintained by the reducer in
-- pq/materialise.go. seq, event_time, and author are the envelope of the last
-- write. A node is never deleted: eviction is a status.
CREATE TABLE IF NOT EXISTS nodes (
    id                      INTEGER PRIMARY KEY,
    name                    TEXT    NOT NULL,
    identifier              TEXT    NOT NULL UNIQUE,
    status                  INTEGER NOT NULL,
    roles                   TEXT    NOT NULL,  -- JSON
    allowed_spaces          TEXT    NOT NULL,  -- JSON
    enrolled_time           INTEGER NOT NULL,  -- epoch ms, 0 until accept
    enrollment_requested_at INTEGER NOT NULL,  -- epoch ms, 0 outside a request
    underlay_address        TEXT    NOT NULL,
    wg_public_key           TEXT    NOT NULL,
    host_addresses          TEXT    NOT NULL,  -- JSON
    host_addresses_unknown  INTEGER NOT NULL,
    created_time            INTEGER NOT NULL,  -- epoch ms
    seq                     INTEGER NOT NULL,
    event_time              INTEGER NOT NULL,  -- epoch ms
    author                  INTEGER NOT NULL
);

-- The newest observed status per node. updated_at is an HLC in unix nanos;
-- the reducer keeps the row with the greater clock. Earlier reports live in
-- the log.
CREATE TABLE IF NOT EXISTS node_status (
    node_id            INTEGER PRIMARY KEY,
    updated_at         INTEGER NOT NULL,
    is_connected       INTEGER NOT NULL,
    last_connected_at  INTEGER NOT NULL,  -- epoch ms
    opendeploy_version TEXT    NOT NULL,
    remote_address     TEXT    NOT NULL,
    runtime_versions   TEXT    NOT NULL,
    seq                INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    author             INTEGER NOT NULL,
    created_time       INTEGER NOT NULL   -- epoch ms of the first row
);
