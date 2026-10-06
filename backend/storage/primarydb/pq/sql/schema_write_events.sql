-- The write log: one envelope per committed global_seq and its mutations in
-- publication order, the same bytes the event stream carries. The entity
-- tables are materialised views of this log; asset_store (node-local
-- placement, reconciled from disk and S3) and global_seq are not. Rows are
-- never updated or deleted and there is no compaction.
CREATE TABLE IF NOT EXISTS write_events (
    seq   INTEGER PRIMARY KEY,
    time  INTEGER NOT NULL,  -- epoch ms
    actor INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS write_event_mutations (
    seq         INTEGER NOT NULL REFERENCES write_events (seq),
    idx         INTEGER NOT NULL,
    entity_type INTEGER NOT NULL,  -- CoreEntityType value
    entity_id   INTEGER NOT NULL,
    op          INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    payload     BLOB,              -- encoded CoreEntity, NULL for a delete
    version      INTEGER,          -- deployment counters carried over from before the reducer derived them
    spec_version INTEGER,          -- NULL on every row written since
    PRIMARY KEY (seq, idx)
);

CREATE INDEX IF NOT EXISTS idx_write_event_mutations_entity ON write_event_mutations (entity_type, entity_id, seq);

-- The payload format of the log: 1 is the v0.0.615 log under api-contract-old,
-- 2 the contract rewritten from the data model. Written once per database.
CREATE TABLE IF NOT EXISTS format_version (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);
