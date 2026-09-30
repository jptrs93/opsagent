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
    PRIMARY KEY (seq, idx)
);

CREATE INDEX IF NOT EXISTS idx_write_event_mutations_entity ON write_event_mutations (entity_type, entity_id, seq);
