-- Materialised views of the write log for the values namespace: one row per
-- live entity in the shape its readers want, rebuilt from the log by
-- RebuildFromLog. Every write goes through the reducer in pq/materialise.go.

-- Next unused id per CoreEntityType, for the tables whose rows are deleted
-- outright and so cannot derive it from MAX(id). Bumped by the reducer on
-- every create or update, read by the writers.
CREATE TABLE IF NOT EXISTS entity_ids (
    entity_type INTEGER PRIMARY KEY,
    next        INTEGER NOT NULL
);

-- Configs and secrets share one file system per space with these folders.
CREATE TABLE IF NOT EXISTS value_directories (
    id           INTEGER PRIMARY KEY,
    space_id     INTEGER NOT NULL,
    parent_id    INTEGER NOT NULL,  -- 0 = the implicit root
    key          TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms
    UNIQUE (space_id, parent_id, key)
);

-- The identity: placement, key, and the newest value version. seq,
-- event_time, and author are the entity's last write of any kind.
CREATE TABLE IF NOT EXISTS secrets (
    id            INTEGER PRIMARY KEY,
    space_id      INTEGER NOT NULL,
    directory_id  INTEGER NOT NULL,  -- 0 = the implicit root
    key           TEXT    NOT NULL,
    value_version INTEGER NOT NULL,
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    created_time  INTEGER NOT NULL,  -- epoch ms
    UNIQUE (space_id, directory_id, key)
);

-- One row per value write, every version of a live secret. The envelope is
-- the write that produced the version; renames and moves never touch it.
CREATE TABLE IF NOT EXISTS secret_versions (
    secret_id     INTEGER NOT NULL,
    value_version INTEGER NOT NULL,
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    smk_version   INTEGER NOT NULL,  -- SMK generation the value is sealed under
    ciphertext    BLOB    NOT NULL,
    nonce         BLOB    NOT NULL,
    PRIMARY KEY (secret_id, value_version)
);

CREATE TABLE IF NOT EXISTS configs (
    id            INTEGER PRIMARY KEY,
    space_id      INTEGER NOT NULL,
    directory_id  INTEGER NOT NULL,  -- 0 = the implicit root
    key           TEXT    NOT NULL,
    value_version INTEGER NOT NULL,
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    created_time  INTEGER NOT NULL,  -- epoch ms
    UNIQUE (space_id, directory_id, key)
);

CREATE TABLE IF NOT EXISTS config_versions (
    config_id     INTEGER NOT NULL,
    value_version INTEGER NOT NULL,
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    value         TEXT    NOT NULL,
    PRIMARY KEY (config_id, value_version)
);

-- The sibling namespace: one row per directory, secret, and config under its
-- path component, which is what makes a key unique across the three tables
-- and resolves a path in one lookup. kind is the CoreEntityType value.
CREATE TABLE IF NOT EXISTS value_keys (
    space_id  INTEGER NOT NULL,
    parent_id INTEGER NOT NULL,  -- 0 = the implicit root
    key       TEXT    NOT NULL,
    kind      INTEGER NOT NULL,
    id        INTEGER NOT NULL,
    PRIMARY KEY (space_id, parent_id, key)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_value_keys_entity ON value_keys (kind, id);
