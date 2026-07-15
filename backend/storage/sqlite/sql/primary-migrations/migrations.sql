-- todo future migrations

-- Node identifiers are immutable mTLS/deployment identities. Existing node
-- names were used for that purpose, so preserve their values during the rename.
ALTER TABLE nodes RENAME COLUMN sni TO identifier;
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_identifier ON nodes(identifier);

ALTER TABLE deployment_configs ADD COLUMN node_id INTEGER NOT NULL DEFAULT -1;
ALTER TABLE deployment_config_history ADD COLUMN node_id INTEGER NOT NULL DEFAULT -1;

UPDATE deployment_configs
SET node_id = COALESCE((
    SELECT nodes.id
    FROM nodes
    WHERE nodes.identifier = deployment_configs.machine
), -1)
WHERE node_id = -1;

UPDATE deployment_config_history
SET node_id = COALESCE((
    SELECT nodes.id
    FROM deployment_configs
    JOIN nodes ON nodes.identifier = deployment_configs.machine
    WHERE deployment_configs.deployment_id = deployment_config_history.deployment_id
), -1)
WHERE node_id = -1;
