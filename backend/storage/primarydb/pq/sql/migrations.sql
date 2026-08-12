-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to the 2026-08 storage split
-- (deployment_status drops, preparer stage split, agent_sessions request/
-- approve columns, nodes.allowed_spaces) were removed after every active
-- cluster had been rolled forward. Upgrading a database from before then
-- requires stepping through a release that still carried them.

-- Asset versions predating user attribution carry created_by = 0 and render as
-- "unknown" in the UI. Every writer now passes the requesting user, so the only
-- rows this can touch are those legacy ones -- attribute them to the first user.
UPDATE asset_versions SET created_by = 1 WHERE created_by = 0;

-- Grant every user that predates the authz layer the builtin cluster_admin
-- template (id 1). The local_kv marker makes this run-once rather than merely
-- idempotent -- without it, a deliberately revoked cluster_admin grant would
-- be silently re-created on the next restart.
INSERT INTO authz_grants (user_id, template_id, created_by, created_at, data_blob)
SELECT u.id, 1, 0, CAST(strftime('%s','now') AS INTEGER) * 1000, X''
FROM users u
WHERE NOT EXISTS (SELECT 1 FROM local_kv WHERE key = 'migration.authz-cluster-admin-grants')
  AND NOT EXISTS (SELECT 1 FROM authz_grants g WHERE g.user_id = u.id AND g.template_id = 1);

INSERT OR IGNORE INTO local_kv (key, value) VALUES ('migration.authz-cluster-admin-grants', X'');
