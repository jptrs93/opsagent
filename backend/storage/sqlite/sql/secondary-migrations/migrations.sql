-- todo future migrations

-- Keep the shared schema compatible with primary node identity records.
ALTER TABLE nodes RENAME COLUMN sni TO identifier;
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_identifier ON nodes(identifier);

ALTER TABLE deployment_configs ADD COLUMN node_id INTEGER NOT NULL DEFAULT -1;
ALTER TABLE deployment_config_history ADD COLUMN node_id INTEGER NOT NULL DEFAULT -1;
