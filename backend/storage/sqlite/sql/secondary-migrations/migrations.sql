-- todo future migrations

-- Keep the shared schema compatible with primary node identity records.
ALTER TABLE nodes RENAME COLUMN sni TO identifier;
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_identifier ON nodes(identifier);
