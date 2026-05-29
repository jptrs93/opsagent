-- add migration here

DROP TABLE IF EXISTS user_config_versions;

-- Merge status_seq_no (HLC, unix nanoseconds) and the redundant epoch-ms
-- timestamp into a single updated_at column. The migration runner tolerates
-- "no such column", so these no-op on a fresh DB (schema.sql already builds
-- the target columns) and only apply to pre-existing DBs upgraded in place.
ALTER TABLE deployment_status         RENAME COLUMN status_seq_no TO updated_at;
ALTER TABLE deployment_status         DROP COLUMN timestamp;
ALTER TABLE deployment_status_history RENAME COLUMN status_seq_no TO updated_at;
ALTER TABLE deployment_status_history DROP COLUMN timestamp;
