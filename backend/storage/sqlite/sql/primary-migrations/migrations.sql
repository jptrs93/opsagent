ALTER TABLE deployment_configs ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;

UPDATE deployment_configs
SET created_at = COALESCE(
        (SELECT created_at FROM deployment_identifiers di WHERE di.id = deployment_configs.deployment_id),
        updated_at)
WHERE created_at = 0;

DELETE FROM deployment_status_history WHERE deployment_id IN (SELECT deployment_id FROM deployment_configs WHERE deleted = 1);
DELETE FROM deployment_status         WHERE deployment_id IN (SELECT deployment_id FROM deployment_configs WHERE deleted = 1);
DELETE FROM deployment_config_history WHERE deployment_id IN (SELECT deployment_id FROM deployment_configs WHERE deleted = 1);
DELETE FROM deployment_configs WHERE deleted = 1;

DROP TABLE IF EXISTS deployment_identifiers;
