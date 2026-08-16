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
-- deployment_configs column drops). Upgrading a database from before then
-- requires stepping through a release that still carried them.

-- 2026-08-15 rebuild the deployment spec version log as deployment_versions,
-- which adds an id primary key (SQLite cannot add a primary-key column in
-- place, and a same-name rebuild would re-fire on every startup, so the table
-- is renamed as part of the change). Schema files run first, so the new empty
-- table already exists and the copy lands in it. The ORDER BY makes id
-- assignment chronological and deterministic. Both statements are no-ops once
-- applied: the source table no longer exists.
INSERT OR IGNORE INTO deployment_versions (deployment_id, version, created_at, created_by, spec_blob)
SELECT deployment_id, version, created_at, created_by, spec_blob
FROM deployment_config_versions
ORDER BY created_at, deployment_id, version;
DROP TABLE IF EXISTS deployment_config_versions;
