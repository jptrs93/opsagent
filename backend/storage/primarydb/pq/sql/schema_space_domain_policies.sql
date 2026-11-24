-- Materialised view of the write log: one row per live space domain policy,
-- maintained by the reducer in pq/materialise.go. data_blob is the
-- SpaceDomainPolicy without its id, which is the key.
CREATE TABLE IF NOT EXISTS space_domain_policies (
    id           INTEGER PRIMARY KEY,
    data_blob    BLOB    NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);
