-- Existing primary deployments have canonical node_id values.

-- A deleted deployment no longer reserves its identity. A later deployment
-- may use the same tuple but receives a new deployment_id.
DROP INDEX IF EXISTS idx_deployment_configs_identity;
CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_active_identity
    ON deployment_configs(space_id, machine, name)
    WHERE deleted = 0;
