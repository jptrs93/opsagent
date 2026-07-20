CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_active_node_identity
    ON deployment_configs(node_id, space_id, name)
    WHERE deleted = 0;

DROP INDEX IF EXISTS idx_deployment_configs_active_identity;
DROP INDEX IF EXISTS idx_deployment_configs_identity;
ALTER TABLE deployment_configs DROP COLUMN machine;
