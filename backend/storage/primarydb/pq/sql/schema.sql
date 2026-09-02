-- Single global write counter: every state-changing write transaction that
-- appends to a version/space log allocates the next value and stamps its rows,
-- so any counter value identifies one cluster-wide state. Rows stamped 0
-- predate the counter.
CREATE TABLE IF NOT EXISTS global_seq (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL
);

INSERT OR IGNORE INTO global_seq (id, value) VALUES (1, 0);

CREATE TABLE IF NOT EXISTS system_config_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,  -- epoch ms
    config_blob BLOB    NOT NULL
);
