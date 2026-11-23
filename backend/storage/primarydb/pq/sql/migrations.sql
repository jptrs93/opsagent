-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: every migration and Go-side startup migration up to and
-- including the v0.0.616 data model conversion was removed on 2026-10-06.
-- pq.Open refuses a database whose format_version row is not the current
-- one and names v0.0.616 as the release to start first. Upgrading an older
-- database means stepping through v0.0.614, v0.0.615, and v0.0.616 in turn.
-- Databases migrated through v0.0.541 keep a dead NULL-only
-- nodes.enrollment_id column: its UNIQUE constraint blocks ALTER TABLE DROP
-- COLUMN, and no query references it.

-- 2026-10-23: per-user passwords, pending users, and link sessions.
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN activated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_sessions ADD COLUMN redeemed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_sessions ADD COLUMN opened_by INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_sessions ADD COLUMN issued_by INTEGER NOT NULL DEFAULT 0;
