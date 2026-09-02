CREATE TABLE IF NOT EXISTS config_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- value rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    created_time       INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author             INTEGER NOT NULL,
    config_id          INTEGER NOT NULL,
    version            INTEGER NOT NULL,  -- top-level: bumps on every event
    value_version      INTEGER NOT NULL,  -- bumps only on value writes
    space_version      INTEGER NOT NULL,  -- bumps only on space moves
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    value              TEXT,              -- NULL unless this event writes the value
    event_type         INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (config_id, version)
);

-- Configs and secrets share ONE file system per space
CREATE TABLE IF NOT EXISTS value_directories (
     id          INTEGER PRIMARY KEY AUTOINCREMENT,
     space_id    INTEGER NOT NULL DEFAULT 1,
     name        TEXT    NOT NULL,
     parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
     created_at  INTEGER NOT NULL,            -- epoch ms
     author  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -author
);
