CREATE TABLE IF NOT EXISTS asset_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id    INTEGER NOT NULL DEFAULT 1,
    key         TEXT    NOT NULL,
    parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -author
);

CREATE TABLE IF NOT EXISTS assets (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id            INTEGER NOT NULL DEFAULT 1,
    key                 TEXT    NOT NULL,
    asset_directory_id  INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at          INTEGER NOT NULL             -- epoch ms
);

CREATE TABLE IF NOT EXISTS asset_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id    INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    sha256      TEXT    NOT NULL DEFAULT '',
    UNIQUE (asset_id, version)
);

CREATE INDEX IF NOT EXISTS idx_asset_versions_sha256 ON asset_versions (sha256);

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
