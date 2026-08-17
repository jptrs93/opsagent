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
-- linger and is harmless). A fourth sweep (2026-08-15, after every active
-- cluster reached v0.0.432) removed the deployment config identity/versions
-- split (deployment_config_history into deployment_config_versions plus the
-- deployment_configs column drops). A fifth sweep (2026-08-17, after every
-- active cluster reached v0.0.433) removed the rebuild of
-- deployment_config_versions into deployment_versions. Upgrading a database
-- from before then requires stepping through a release that still carried
-- them.

-- 2026-08-17 split scheduled instance state into the append-only
-- scheduled_instance_versions log (schema files run first, so the empty table
-- already exists). The backfill gives every existing instance a single
-- baseline row claiming its current state from birth — pre-split transitions
-- were never recorded. created_at and state then move off scheduled_instances
-- entirely, derived from the first/latest version rows instead. The unique_run
-- index references state and must be dropped before the column can be. Re-runs
-- are no-ops: the backfill's column references fail as "no such column" once
-- the drops have applied.
INSERT OR IGNORE INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state)
SELECT id, 1, created_at, state FROM scheduled_instances ORDER BY id;
ALTER TABLE scheduled_instances DROP COLUMN created_at;
DROP INDEX IF EXISTS idx_scheduled_instances_node_active;
DROP INDEX IF EXISTS idx_scheduled_instances_unique_run;
ALTER TABLE scheduled_instances DROP COLUMN state;
