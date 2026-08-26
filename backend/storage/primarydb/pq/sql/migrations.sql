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
