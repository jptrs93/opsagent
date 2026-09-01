-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.541 (2026-08-31),
-- ending with the node_versions split and the merge of enrollment_requests
-- into nodes, were removed after every active cluster had been rolled
-- forward. The spec-version column renames and the one-time deployment event
-- log migration (v0.0.549) were removed likewise after the v0.0.550 rollout.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS deployment_spec_versions;
DROP TABLE IF EXISTS deployment_space_versions;
ALTER TABLE scheduled_instances DROP COLUMN deployment_space_version_id;

ALTER TABLE deployment_event_log RENAME COLUMN created_at TO event_time;
ALTER TABLE deployment_event_log ADD COLUMN created_time INTEGER NOT NULL DEFAULT 0;
UPDATE deployment_event_log SET created_time = (
    SELECT e2.event_time FROM deployment_event_log e2
    WHERE e2.deployment_id = deployment_event_log.deployment_id AND e2.version = 1
) WHERE created_time = 0;
