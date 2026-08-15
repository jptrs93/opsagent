CREATE TABLE IF NOT EXISTS secret_config_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id    INTEGER NOT NULL DEFAULT 1,
    name        TEXT    NOT NULL,
    parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    updated_at  INTEGER NOT NULL,            -- epoch ms
    updated_by  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -created_by
);

CREATE TABLE IF NOT EXISTS config_displays (
    id                 INTEGER PRIMARY KEY,
    name               TEXT    NOT NULL,
    directory_id       INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    updated_at  INTEGER NOT NULL,            -- epoch ms
    updated_by  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -created_by
);

