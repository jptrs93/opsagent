-- Add migrations here

-- Drop the pre-scheduled-instance status tables. Every deployment's runtime
-- state now lives in scheduled_instances / scheduled_instance_status, and the
-- one-shot migration that read these has been removed.
DROP TABLE IF EXISTS deployment_status;
DROP TABLE IF EXISTS deployment_status_history;
DELETE FROM local_kv WHERE key = 'scheduled_instance_migration_v1';
