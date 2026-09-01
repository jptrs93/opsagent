-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.541 (2026-08-31),
-- ending with the node_versions split and the merge of enrollment_requests
-- into nodes, were removed after every active cluster had been rolled
-- forward. Upgrading a database from before then requires stepping through a
-- release that still carried them. Databases migrated through v0.0.541 keep
-- a dead NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

ALTER TABLE scheduled_instances RENAME COLUMN deployment_version TO deployment_spec_version;
ALTER TABLE scheduled_instance_status RENAME COLUMN preparer_config_version TO preparer_spec_version;
ALTER TABLE scheduled_instance_status RENAME COLUMN runner_config_version TO runner_spec_version;
