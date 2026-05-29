-- add migration here

-- Align legacy secondary column names with the unified schema (schema.sql).
-- The migration runner tolerates "no such column" / "duplicate column name",
-- so these are no-ops on a freshly-created secondary DB (which schema.sql
-- already builds with the target columns) and only apply to pre-existing
-- secondary DBs upgraded in place.
ALTER TABLE deployment_configs        RENAME COLUMN seq_no          TO version;
ALTER TABLE deployment_configs        ADD COLUMN    environment TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_configs        ADD COLUMN    name        TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_status         RENAME COLUMN preparer_seq_no TO preparer_config_version;
ALTER TABLE deployment_status         RENAME COLUMN runner_seq_no   TO runner_config_version;
ALTER TABLE deployment_status_history RENAME COLUMN preparer_seq_no TO preparer_config_version;
ALTER TABLE deployment_status_history RENAME COLUMN runner_seq_no   TO runner_config_version;

-- Merge status_seq_no (HLC, unix nanoseconds) and the redundant epoch-ms
-- timestamp into a single updated_at column (see primary-migrations).
ALTER TABLE deployment_status         RENAME COLUMN status_seq_no TO updated_at;
ALTER TABLE deployment_status         DROP COLUMN timestamp;
ALTER TABLE deployment_status_history RENAME COLUMN status_seq_no TO updated_at;
ALTER TABLE deployment_status_history DROP COLUMN timestamp;
