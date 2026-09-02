CREATE TABLE IF NOT EXISTS asset_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- content rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    created_time       INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author             INTEGER NOT NULL,
    asset_id           INTEGER NOT NULL,
    version            INTEGER NOT NULL,  -- top-level: bumps on every event
    value_version      INTEGER NOT NULL,  -- bumps only on content writes
    space_version      INTEGER NOT NULL,  -- bumps only on space moves
    key                TEXT    NOT NULL,
    asset_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    size_bytes         INTEGER,           -- NULL unless this event writes content
    sha256             TEXT,              -- NULL unless this event writes content
    event_type         INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (asset_id, version)
);

CREATE INDEX IF NOT EXISTS idx_asset_event_log_sha256
    ON asset_event_log (sha256) WHERE sha256 IS NOT NULL;

-- tracks where the asset content is actually stored
CREATE TABLE IF NOT EXISTS asset_store (
    id            TEXT    PRIMARY KEY,
    sha256        TEXT    NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    inline_blob   BLOB    NOT NULL DEFAULT x'',
    local_status  INTEGER NOT NULL DEFAULT 0,
    remote_status INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL             -- epoch ms
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_store_sha256 ON asset_store (sha256) WHERE sha256 != '';

CREATE TABLE IF NOT EXISTS asset_migrations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    old_config_version_id INTEGER NOT NULL,
    new_config_version_id INTEGER NOT NULL,
    status                TEXT    NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'running', 'finished')),
    last_error            TEXT    NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,               -- epoch ms
    started_at            INTEGER NOT NULL DEFAULT 0,     -- epoch ms
    last_attempt_at       INTEGER NOT NULL DEFAULT 0,     -- epoch ms
    finished_at           INTEGER NOT NULL DEFAULT 0      -- epoch ms
);

-- At most one unfinished migration at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_migrations_unfinished
    ON asset_migrations ((1))
    WHERE status != 'finished';

CREATE TABLE IF NOT EXISTS asset_directories (
     id          INTEGER PRIMARY KEY AUTOINCREMENT,
     space_id    INTEGER NOT NULL DEFAULT 1,
     key         TEXT    NOT NULL,
     parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
     created_at  INTEGER NOT NULL,            -- epoch ms
     author  INTEGER NOT NULL DEFAULT 0       -- user id; 0 = unknown/system, negative = agent of user -author
);