-- Materialised views of the write log for the assets namespace, the same
-- shape as the values namespace in schema_values.sql: a directory tree, an
-- identity row per asset, every content version of a live asset, and the
-- sibling namespace that keeps keys unique across assets and directories.

CREATE TABLE IF NOT EXISTS asset_directories (
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

CREATE TABLE IF NOT EXISTS assets (
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

CREATE TABLE IF NOT EXISTS asset_versions (
    asset_id      INTEGER NOT NULL,
    value_version INTEGER NOT NULL,
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL,
    sha256        TEXT    NOT NULL,
    size_bytes    INTEGER NOT NULL,
    storage_key   TEXT    NOT NULL,  -- physical name of the content (asset_store.id)
    PRIMARY KEY (asset_id, value_version)
);

CREATE INDEX IF NOT EXISTS idx_asset_versions_sha256 ON asset_versions (sha256);

CREATE INDEX IF NOT EXISTS idx_asset_versions_storage_key ON asset_versions (storage_key);

CREATE TABLE IF NOT EXISTS asset_keys (
    space_id  INTEGER NOT NULL,
    parent_id INTEGER NOT NULL,  -- 0 = the implicit root
    key       TEXT    NOT NULL,
    kind      INTEGER NOT NULL,  -- CoreEntityType value
    id        INTEGER NOT NULL,
    PRIMARY KEY (space_id, parent_id, key)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_keys_entity ON asset_keys (kind, id);

-- This node's placement of each content blob: which side holds a durable copy.
-- Not a materialisation of the log: the identity of the content (id = storage
-- key, sha256, size) is also on every asset_versions row, but the status flags
-- are local knowledge reconciled from disk and S3.
CREATE TABLE IF NOT EXISTS asset_store (
    id            TEXT    PRIMARY KEY,         -- storage key: LargeAssetsDir/<id> locally, <s3-path>/<id> in S3
    sha256        TEXT    NOT NULL DEFAULT '', -- '' while an upload is staging
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    local_status  INTEGER NOT NULL DEFAULT 0,
    remote_status INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL             -- epoch ms
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_store_sha256 ON asset_store (sha256) WHERE sha256 != '';
