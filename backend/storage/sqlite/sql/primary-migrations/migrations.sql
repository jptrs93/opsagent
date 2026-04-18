-- Primary-side startup migrations. Each statement should be idempotent —
-- this file is executed on every primary startup after the schema has been
-- applied. Remove entries from this file once the bad data they target has
-- been cleaned up in all deployments.
--
-- Every `?` placeholder in a statement is bound to the primary's machine
-- name (see applyPrimaryMigrations in backend/storage/sqlite/adapter.go).

-- 2026-04: drop single-column history indexes that duplicated the leftmost
-- prefix of each table's composite primary key. They cost write
-- amplification on every history insert without providing any query the
-- PK index didn't already cover.
DROP INDEX IF EXISTS idx_config_history_deployment;
DROP INDEX IF EXISTS idx_status_history_deployment;
