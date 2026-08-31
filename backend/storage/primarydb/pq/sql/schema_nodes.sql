CREATE TABLE IF NOT EXISTS nodes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at    INTEGER NOT NULL DEFAULT 0,
    enrolled_at   INTEGER NOT NULL DEFAULT 0,
    name          TEXT    NOT NULL,
    identifier    TEXT    NOT NULL DEFAULT '',
    UNIQUE(name),
    UNIQUE(identifier)
);

CREATE TABLE IF NOT EXISTS node_versions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id        INTEGER NOT NULL,
    version        INTEGER NOT NULL,
    created_at     INTEGER NOT NULL,
    author         INTEGER NOT NULL DEFAULT 0,
    status         INTEGER NOT NULL DEFAULT 0,
    roles          TEXT    NOT NULL DEFAULT '[]',
    addresses      TEXT    NOT NULL DEFAULT '[]',
    wg_public_key  TEXT    NOT NULL DEFAULT '',
    allowed_spaces TEXT    NOT NULL DEFAULT '[0]',
    global_seq     INTEGER NOT NULL DEFAULT 0,
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
