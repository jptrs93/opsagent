-- Single global write counter: every state-changing write transaction that
-- appends to a version/space log allocates the next value and stamps its rows,
-- so any counter value identifies one cluster-wide state. Rows stamped 0
-- predate the counter.
CREATE TABLE IF NOT EXISTS global_seq (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL
);

INSERT OR IGNORE INTO global_seq (id, value) VALUES (1, 0);

-- Append-only: one row per settings revision, the row id is the version.
CREATE TABLE IF NOT EXISTS system_config_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,  -- epoch ms
    author      INTEGER NOT NULL,
    event_type  INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update
    config_blob BLOB    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_system_config_event_log_seq ON system_config_event_log (global_seq, id);
