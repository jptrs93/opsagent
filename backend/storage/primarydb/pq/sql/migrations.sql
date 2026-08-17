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
-- plus the scheduled_instances created_at/state column and index drops). A
-- seventh sweep (2026-08-17, after every active cluster reached v0.0.435 and
-- its converter had hashed away all 'legacy:' placeholder shas) removed the
-- asset content split (the asset_store backfill, the asset_versions sha256
-- column add and blob/location drops, and the background legacy-sha
-- converter). An eighth sweep (2026-08-17, after every active cluster reached
-- v0.0.437) removed the authz rule template identity/versions split (the
-- authz_rule_template_versions backfill and authz_rule_templates column
-- drops) and the authz_grants deleted column add. A ninth sweep (2026-08-17,
-- after every active cluster reached v0.0.440) removed the created_by to
-- author column renames across twelve tables and the container-level author
-- column drops on assets/secrets/configs. Upgrading a database from before
-- then requires stepping through a release that still carried them.

-- Purge rows soft-deleted under the old deleted flag before it becomes
-- deleted_at. The rename doubles as the backfill: every surviving row has
-- deleted = 0. Purged deployments take their version and scheduled-instance
-- history with them so a later reuse of a freed deployment_id cannot inherit
-- stale rows.
DELETE FROM scheduled_instance_status WHERE scheduled_instance_id IN (
    SELECT id FROM scheduled_instances WHERE deployment_id IN (
        SELECT deployment_id FROM deployment_configs WHERE deleted = 1));
DELETE FROM scheduled_instance_versions WHERE scheduled_instance_id IN (
    SELECT id FROM scheduled_instances WHERE deployment_id IN (
        SELECT deployment_id FROM deployment_configs WHERE deleted = 1));
DELETE FROM scheduled_instances WHERE deployment_id IN (
    SELECT deployment_id FROM deployment_configs WHERE deleted = 1);
DELETE FROM deployment_versions WHERE deployment_id IN (
    SELECT deployment_id FROM deployment_configs WHERE deleted = 1);
DELETE FROM deployment_configs WHERE deleted = 1;
ALTER TABLE deployment_configs RENAME COLUMN deleted TO deleted_at;
DELETE FROM authz_rule_template_versions WHERE template_id IN (
    SELECT id FROM authz_rule_templates WHERE deleted = 1);
DELETE FROM authz_rule_templates WHERE deleted = 1;
ALTER TABLE authz_rule_templates RENAME COLUMN deleted TO deleted_at;
DELETE FROM authz_grants WHERE deleted = 1;
ALTER TABLE authz_grants RENAME COLUMN deleted TO deleted_at;

ALTER TABLE assets ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE secrets ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE configs ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;

-- Space assignments move into append-only *_spaces logs. The backfill writes
-- each existing row's current space as its initial assignment (author 0 =
-- unknown, stamped with the container's creation time), then the denormalized
-- column drops. Re-runs fail on the dropped space_id column and are tolerated.
INSERT INTO asset_spaces (asset_id, author, created_at, space_id)
SELECT id, 0, created_at, space_id FROM assets
WHERE id NOT IN (SELECT asset_id FROM asset_spaces);
ALTER TABLE assets DROP COLUMN space_id;
INSERT INTO secret_spaces (secret_id, author, created_at, space_id)
SELECT id, 0, created_at, space_id FROM secrets
WHERE id NOT IN (SELECT secret_id FROM secret_spaces);
ALTER TABLE secrets DROP COLUMN space_id;
INSERT INTO config_spaces (config_id, author, created_at, space_id)
SELECT id, 0, created_at, space_id FROM configs
WHERE id NOT IN (SELECT config_id FROM config_spaces);
ALTER TABLE configs DROP COLUMN space_id;
