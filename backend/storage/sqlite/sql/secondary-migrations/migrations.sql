-- Config snapshots received after the node ID rollout provide the authoritative
-- ID. Apply it to older configs for the same machine only when that mapping is
-- unambiguous.
UPDATE deployment_configs AS target
SET node_id = (
    SELECT MIN(source.node_id)
    FROM deployment_configs AS source
    WHERE source.machine = target.machine AND source.node_id > 0
)
WHERE target.node_id <= 0
  AND (
      SELECT COUNT(DISTINCT source.node_id)
      FROM deployment_configs AS source
      WHERE source.machine = target.machine AND source.node_id > 0
  ) = 1;

-- Deployment history does not need to duplicate the current config's node ID.
ALTER TABLE deployment_config_history DROP COLUMN node_id;
