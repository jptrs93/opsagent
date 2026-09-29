CREATE TABLE IF NOT EXISTS asset_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- content rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    created_time       INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author             INTEGER NOT NULL,
    asset_id           INTEGER NOT NULL,
    version            INTEGER NOT NULL,  -- top-level: bumps on every event
    value_version      INTEGER NOT NULL,  -- bumps only on content writes
    value_changed      INTEGER NOT NULL DEFAULT 0,  -- 1 iff this event bumped value_version
    key                TEXT    NOT NULL,
    asset_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    size_bytes         INTEGER NOT NULL,  -- current content size, carried forward on non-content events
    sha256             TEXT    NOT NULL,  -- current content hash, carried forward on non-content events
    storage_key        TEXT    NOT NULL DEFAULT '',  -- physical name of the content (asset_store.id), carried forward
    event_type         INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (asset_id, version)
);

-- A value reference (asset_id, value_version) names exactly one value-changing row.
CREATE UNIQUE INDEX IF NOT EXISTS asset_value_versions
    ON asset_event_log (asset_id, value_version) WHERE value_changed != 0;

-- Content-writing rows only: one per (asset_id, value_version).
CREATE INDEX IF NOT EXISTS idx_asset_event_log_sha256
    ON asset_event_log (sha256) WHERE value_changed != 0;

-- This node's placement of each content blob: which side holds a durable copy.
-- The identity of the content (id = storage key, sha256, size) is also carried
-- on every asset_event_log row; only the status flags are local knowledge.
CREATE TABLE IF NOT EXISTS asset_store (
    id            TEXT    PRIMARY KEY,         -- storage key: LargeAssetsDir/<id> locally, <s3-path>/<id> in S3
    sha256        TEXT    NOT NULL DEFAULT '', -- '' while an upload is staging
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    local_status  INTEGER NOT NULL DEFAULT 0,
    remote_status INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL             -- epoch ms
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_store_sha256 ON asset_store (sha256) WHERE sha256 != '';

-- Append-only: one row per directory event, live state is the newest row per
-- directory_id whose event_type is not delete.
CREATE TABLE IF NOT EXISTS asset_directory_event_log (
     id           INTEGER PRIMARY KEY AUTOINCREMENT,
     global_seq   INTEGER NOT NULL,
     event_time   INTEGER NOT NULL,   -- epoch ms
     author       INTEGER NOT NULL,   -- user id; 0 system, negative = agent of user -author
     directory_id INTEGER NOT NULL,
     event_type   INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update / 3 delete
     space_id     INTEGER NOT NULL DEFAULT 1,
     key          TEXT    NOT NULL,
     parent_id    INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
     created_at   INTEGER NOT NULL             -- epoch ms, copied forward
);

CREATE INDEX IF NOT EXISTS idx_asset_directory_event_log_directory_id ON asset_directory_event_log (directory_id, id);