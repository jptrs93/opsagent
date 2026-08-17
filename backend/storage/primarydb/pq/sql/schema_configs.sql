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
    space_id           INTEGER NOT NULL DEFAULT 1,
    value_directory_id INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at         INTEGER NOT NULL,            -- epoch ms
    author         INTEGER NOT NULL DEFAULT 0   -- user id
);

CREATE TABLE IF NOT EXISTS config_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id   INTEGER NOT NULL,
    version     INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    value       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    UNIQUE (config_id, version)
);
