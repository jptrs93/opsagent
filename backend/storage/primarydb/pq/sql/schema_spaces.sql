-- Materialised view of the write log: one row per live space, maintained by
-- the reducer in pq/materialise.go. Spaces 0 (_system) and 1 (global) are
-- written at seq 0 of a fresh database by pq.Open. seq, event_time, and
-- author are the envelope of the last write.
CREATE TABLE IF NOT EXISTS spaces (
    id         INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535),
    name       TEXT    NOT NULL DEFAULT '',
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL,  -- user id; 0 system, negative = agent of user -author
    created_time INTEGER NOT NULL   -- epoch ms of the first row
);
