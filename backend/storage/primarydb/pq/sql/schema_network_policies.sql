-- Materialised view of the write log: one row per live network policy,
-- maintained by the reducer in pq/materialise.go. data_blob is the
-- NetworkPolicy without its created_time, which is the column.
CREATE TABLE IF NOT EXISTS network_policies (
    id           INTEGER PRIMARY KEY,
    data_blob    BLOB    NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);
