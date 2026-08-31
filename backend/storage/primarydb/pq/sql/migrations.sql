-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.510 (2026-08-19),
-- ending with the drop of the retired desired-state columns, were removed
-- after every active cluster had been rolled forward. Upgrading a database
-- from before then requires stepping through a release that still carried
-- them.

ALTER TABLE scheduled_instance_status ADD COLUMN runner_exit_code INTEGER;

ALTER TABLE nodes ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;

INSERT INTO node_versions (node_id, version, created_at, author, status, roles, addresses, wg_public_key, allowed_spaces, global_seq)
SELECT id, 1, enrolled_at, 0, 4, roles, addresses, wg_public_key, allowed_spaces, 0
FROM nodes
WHERE NOT EXISTS (SELECT 1 FROM node_versions v WHERE v.node_id = nodes.id);

UPDATE nodes SET created_at = enrolled_at WHERE created_at = 0 AND enrolled_at > 0;

ALTER TABLE nodes DROP COLUMN roles;

ALTER TABLE nodes DROP COLUMN addresses;

ALTER TABLE nodes DROP COLUMN wg_public_key;

ALTER TABLE nodes DROP COLUMN allowed_spaces;

ALTER TABLE node_statuses ADD COLUMN opendeploy_version TEXT NOT NULL DEFAULT '';

ALTER TABLE node_statuses ADD COLUMN remote_address TEXT NOT NULL DEFAULT '';

ALTER TABLE node_statuses ADD COLUMN enrollment_pending INTEGER NOT NULL DEFAULT 0;

DROP TABLE IF EXISTS enrollment_requests;
