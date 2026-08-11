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
