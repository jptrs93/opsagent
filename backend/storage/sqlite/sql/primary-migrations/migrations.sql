-- Size metadata allows S3-backed asset rows to list their original byte count
-- without loading the object. Existing inline rows backfill from blob length.
ALTER TABLE assets ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE assets SET size_bytes = length(blob) WHERE size_bytes = 0;

-- Runner networking status fields are persisted as a compact protobuf blob so
-- endpoint/diagnostic schema can evolve without adding SQL columns per field.
ALTER TABLE deployment_status ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
ALTER TABLE deployment_status_history ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';

-- Node registry metadata and live status are separate so node identity can be
-- streamed independently from connection state.
ALTER TABLE nodes ADD COLUMN addresses TEXT NOT NULL DEFAULT '[]';
ALTER TABLE nodes ADD COLUMN enrolled_at INTEGER NOT NULL DEFAULT 0;
UPDATE nodes SET enrolled_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000 WHERE enrolled_at = 0;
CREATE TABLE IF NOT EXISTS node_statuses (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id           INTEGER NOT NULL,
    last_connected_at INTEGER NOT NULL DEFAULT 0,
    is_connected      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(node_id)
);
INSERT OR IGNORE INTO node_statuses (node_id, last_connected_at, is_connected)
SELECT id, 0, 0 FROM nodes
