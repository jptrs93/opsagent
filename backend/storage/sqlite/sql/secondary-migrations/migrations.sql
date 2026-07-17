-- Existing secondary caches have canonical node_id values.

-- Keep the worker cache constraint aligned with the primary. Deleted cached
-- rows must not reserve an identity used by a newer independent deployment.
DROP INDEX IF EXISTS idx_deployment_configs_identity;
CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_active_identity
    ON deployment_configs(space_id, machine, name)
    WHERE deleted = 0;
