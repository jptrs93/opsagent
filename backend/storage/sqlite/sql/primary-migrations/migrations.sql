-- Deployment history does not need to duplicate the current config's node ID.
ALTER TABLE deployment_config_history DROP COLUMN node_id;
