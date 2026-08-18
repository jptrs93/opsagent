-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.503 (2026-08-18),
-- ending with the deployment_space_versions space-split backfill and the
-- scheduled_instances deployment_space_version_id add, were removed after
-- every active cluster had been rolled forward. Upgrading a database from
-- before then requires stepping through a release that still carried them.

INSERT OR IGNORE INTO deployments (deployment_id, node_id, name, deleted_at)
SELECT deployment_id, node_id, name, deleted_at FROM deployment_configs;
DROP TABLE IF EXISTS deployment_configs;

ALTER TABLE deployment_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployment_space_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scheduled_instance_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE asset_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE asset_spaces ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE config_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE config_spaces ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE secret_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE secret_spaces ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE authz_rule_template_versions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
