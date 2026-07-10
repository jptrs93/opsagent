-- Runner networking status fields are persisted as a compact protobuf blob so
-- endpoint/diagnostic schema can evolve without adding SQL columns per field.
ALTER TABLE deployment_status ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
ALTER TABLE deployment_status_history ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';

-- Keep shared schema-compatible if a secondary database already has node rows.
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
