-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.506 (2026-08-18),
-- ending with the global_access_rules deleted_at add, were removed after
-- every active cluster had been rolled forward. Upgrading a database from
-- before then requires stepping through a release that still carried them.

DROP TABLE IF EXISTS local_kv;
