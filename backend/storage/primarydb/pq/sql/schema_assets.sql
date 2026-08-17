CREATE TABLE IF NOT EXISTS asset_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id    INTEGER NOT NULL DEFAULT 1,
    key         TEXT    NOT NULL,
    parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at  INTEGER NOT NULL,            -- epoch ms
    created_by  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -created_by
);

CREATE TABLE IF NOT EXISTS assets (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id            INTEGER NOT NULL DEFAULT 1,
    key                 TEXT    NOT NULL,
    asset_directory_id  INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at          INTEGER NOT NULL,            -- epoch ms
    created_by          INTEGER NOT NULL DEFAULT 0   -- user id
);

-- Small versions store content inline in blob; large versions store their object reference in location.
CREATE TABLE IF NOT EXISTS asset_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id    INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,            -- epoch ms
    created_by  INTEGER NOT NULL DEFAULT 0,  -- user id
    location    TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    blob        BLOB    NOT NULL,
    UNIQUE (asset_id, version)
);

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
