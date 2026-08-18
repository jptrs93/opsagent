-- Configs and secrets share ONE file system per space
CREATE TABLE IF NOT EXISTS value_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id    INTEGER NOT NULL DEFAULT 1,
    name        TEXT    NOT NULL,
    parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -author
);

CREATE TABLE IF NOT EXISTS configs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at         INTEGER NOT NULL,            -- epoch ms
    deleted_at         INTEGER NOT NULL DEFAULT 0   -- epoch ms, 0 = not deleted
);

-- Append-only log of space assignments; the newest row is the config's current
-- space. Creation writes the first row.
CREATE TABLE IF NOT EXISTS config_spaces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id   INTEGER NOT NULL,
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    created_at  INTEGER NOT NULL,            -- epoch ms
    space_id    INTEGER NOT NULL,
    global_seq  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_config_spaces_config ON config_spaces (config_id);

CREATE TABLE IF NOT EXISTS config_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id   INTEGER NOT NULL,
    version     INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    value       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    global_seq  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (config_id, version)
);
