ALTER TABLE deployment_configs ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;

DROP TABLE IF EXISTS deployment_identifiers;
