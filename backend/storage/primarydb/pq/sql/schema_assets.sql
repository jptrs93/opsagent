-- Assets are stable identities (space, directory, key) whose content lives in
-- immutable numbered versions. Deployment configs pin asset_versions.id; the
-- assets.id survives renames, moves, and new versions.
--
-- Each space is an independent file system. Path uniqueness — no two siblings
-- (assets or directories) sharing a key under one parent — spans two tables, so
-- it cannot be a SQL constraint. Every create/rename/move MUST go through the
-- storage layer's asset namespace mutex, which checks both tables.
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
    created_by          INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -created_by
);

-- Version rows are immutable: editing an asset appends the next version. Small
-- versions store content inline in blob; large versions store their object
-- reference in location.
CREATE TABLE IF NOT EXISTS asset_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id    INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,            -- epoch ms; creation time of this version
    created_by  INTEGER NOT NULL DEFAULT 0,  -- user id; 0 = unknown/system, negative = agent of user -created_by
    location    TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    blob        BLOB    NOT NULL,
    UNIQUE (asset_id, version)
);

-- One durable job records each complete large-asset storage transition. Version
-- locations remain authoritative for per-asset progress.
CREATE TABLE IF NOT EXISTS asset_migrations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    old_config_version_id INTEGER NOT NULL,
    new_config_version_id INTEGER NOT NULL,
    status                TEXT    NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'running', 'finished')),
    last_error            TEXT    NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    started_at            INTEGER NOT NULL DEFAULT 0,
    last_attempt_at       INTEGER NOT NULL DEFAULT 0,
    finished_at           INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_migrations_unfinished
    ON asset_migrations ((1))
    WHERE status != 'finished';
