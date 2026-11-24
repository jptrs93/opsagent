CREATE TABLE IF NOT EXISTS node_access_policies (
    id           INTEGER PRIMARY KEY,
    data_blob    BLOB    NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);
