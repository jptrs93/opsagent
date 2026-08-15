-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to the 2026-08 storage split
-- (deployment_status drops, preparer stage split, agent_sessions request/
-- approve columns, nodes.allowed_spaces) were removed after every active
-- cluster had been rolled forward. A second sweep (2026-08-13) removed the
-- asset_versions created_by attribution, the run-once authz cluster_admin
-- grant backfill (its local_kv marker 'migration.authz-cluster-admin-grants'
-- may linger in older databases and is harmless), and the users created_at /
-- last_login_at column adds and backfill. A third sweep (2026-08-15, after
-- every active cluster reached v0.0.426) removed the run-once authz verb
-- renumber fixup (its local_kv marker 'migration.authz-verb-renumber' may
-- linger and is harmless). Upgrading a database from before
-- then requires stepping through a release that still carried them.

-- 2026-08-15 deployment config identity/versions split. The append-only
-- deployment_config_history becomes deployment_config_versions (schema files
-- run first, so the new table already exists and the copy lands in it), and
-- deployment_configs is slimmed to the stable identity columns. The guard copy
-- from deployment_configs covers a current version that somehow never reached
-- history. Both copies and the drops are no-ops once applied (PK conflicts are
-- ignored and missing tables/columns are tolerated).
INSERT OR IGNORE INTO deployment_config_versions (deployment_id, version, created_at, created_by, spec_blob)
SELECT deployment_id, version, updated_at, updated_by, spec_blob FROM deployment_config_history;
INSERT OR IGNORE INTO deployment_config_versions (deployment_id, version, created_at, created_by, spec_blob)
SELECT deployment_id, version, updated_at, updated_by, spec_blob FROM deployment_configs;
DROP TABLE IF EXISTS deployment_config_history;
ALTER TABLE deployment_configs DROP COLUMN version;
ALTER TABLE deployment_configs DROP COLUMN created_at;
ALTER TABLE deployment_configs DROP COLUMN updated_at;
ALTER TABLE deployment_configs DROP COLUMN updated_by;
ALTER TABLE deployment_configs DROP COLUMN spec_blob;
