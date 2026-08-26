-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.507 (2026-08-18), the
-- storage split (deployment_status drops, preparer stage split, and the sweep
-- of legacy primary-side tables off worker databases), were removed after
-- every active cluster had been rolled forward. Upgrading a database from
-- before then requires stepping through a release that still carried them.

ALTER TABLE scheduled_instance_status ADD COLUMN runner_exit_code INTEGER;
