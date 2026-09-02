-- One append-only event log per node, one row per event; version 1 is the
-- creation write. Identity facets (name, identifier) are denormalised onto
-- every row, so the highest-version row is the complete current state;
-- created_time is the first event's event_time copied to every row and
-- enrolled_time is 0 until the enrollment-accept event stamps it, copied
-- forward from there. Node ids are allocated as MAX(node_id)+1 under the
-- service mutex; the log is never pruned, so ids are never reused. Name and
-- identifier uniqueness cannot be SQL constraints on a log — both are
-- enforced by latest-row scans under the service mutex.
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
    UNIQUE(node_id)
);
