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
-- deployment_config_versions into deployment_versions. A sixth sweep
-- (2026-08-17, after every active cluster reached v0.0.434) removed the
-- scheduled instance state split (the scheduled_instance_versions backfill
-- plus the scheduled_instances created_at/state column and index drops).
-- Upgrading a database from before then requires stepping through a release
-- that still carried them.

DELETE FROM asset_versions WHERE location LIKE 'pending://%';
DELETE FROM assets WHERE id NOT IN (SELECT asset_id FROM asset_versions);
INSERT OR IGNORE INTO asset_store (id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at)
SELECT CAST(id AS TEXT), 'legacy:' || id, size_bytes, blob,
       CASE WHEN location LIKE 'local://%' THEN 1 ELSE 0 END,
       CASE WHEN location LIKE 's3://%' THEN 1 ELSE 0 END,
       created_at
FROM asset_versions;
ALTER TABLE asset_versions ADD COLUMN sha256 TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_asset_versions_sha256 ON asset_versions (sha256);
UPDATE asset_versions SET sha256 = 'legacy:' || id WHERE sha256 = '' AND location IS NOT NULL;
ALTER TABLE asset_versions DROP COLUMN blob;
ALTER TABLE asset_versions DROP COLUMN location;
