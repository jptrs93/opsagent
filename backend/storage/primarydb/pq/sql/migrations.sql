-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.507 (2026-08-18),
-- ending with the primary local_kv table drop, were removed after every
-- active cluster had been rolled forward. Upgrading a database from before
-- then requires stepping through a release that still carried them.

-- v0.0.510: drop the retired desired-state columns. Workload state moved onto
-- the spec long ago but the columns were only abandoned, never dropped, so
-- databases created before the sweep still carry them.
ALTER TABLE deployments DROP COLUMN desired_version;
ALTER TABLE deployments DROP COLUMN desired_running;
ALTER TABLE deployment_versions DROP COLUMN desired_version;
ALTER TABLE deployment_versions DROP COLUMN desired_running;
