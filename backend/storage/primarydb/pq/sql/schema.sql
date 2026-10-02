-- Single global write counter: every state-changing write transaction that
-- appends to a version/space log allocates the next value and stamps its rows,
-- so any counter value identifies one cluster-wide state. Rows stamped 0
-- predate the counter.
CREATE TABLE IF NOT EXISTS global_seq (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL
);

INSERT OR IGNORE INTO global_seq (id, value) VALUES (1, 0);

-- Materialised view of the write log: the one live settings document,
-- maintained by the reducer in pq/materialise.go. Earlier revisions live in
-- the log. seq, event_time, and author are the envelope of the last write.
CREATE TABLE IF NOT EXISTS system_config (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    config_blob BLOB    NOT NULL,
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL,
    created_time INTEGER NOT NULL   -- epoch ms of the first row
);
