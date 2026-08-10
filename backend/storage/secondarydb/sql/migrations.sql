-- Add migrations here

-- Drop the pre-scheduled-instance status tables. Every deployment's runtime
-- state now lives in scheduled_instances / scheduled_instance_status, and the
-- one-shot migration that read these has been removed.
DROP TABLE IF EXISTS deployment_status;
DROP TABLE IF EXISTS deployment_status_history;
DELETE FROM local_kv WHERE key = 'scheduled_instance_migration_v1';

-- Split the single preparer status into per-stage statuses. The rollup is now
-- derived from the stages on read, so preparer_status is backfilled into them
-- and then dropped.
ALTER TABLE scheduled_instance_status ADD COLUMN preparer_inputs_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scheduled_instance_status ADD COLUMN preparer_image_status INTEGER NOT NULL DEFAULT 0;

-- Backfill: every legacy rollup maps to exactly one stage pair that re-derives
-- to itself. Inputs are assumed to have succeeded, since a rollup past UNKNOWN
-- means the image stage had been reached. The one thing not recoverable is
-- which stage a legacy FAILED failed in, attributed here to the image.
--   PREPARING(2)->BUILDING(1)  DOWNLOADING(3)->DOWNLOADING(3)
--   READY(4)->READY(4)  FAILED(5)->FAILED(5)  PULLING(6)->PULLING(2)
-- The WHERE keeps this off rows that already carry real stage detail.
UPDATE scheduled_instance_status
SET preparer_inputs_status = CASE WHEN COALESCE(preparer_status, 0) = 0 THEN 0 ELSE 2 END,
    preparer_image_status = CASE COALESCE(preparer_status, 0)
        WHEN 2 THEN 1
        WHEN 3 THEN 3
        WHEN 4 THEN 4
        WHEN 5 THEN 5
        WHEN 6 THEN 2
        ELSE 0
    END
WHERE preparer_inputs_status = 0 AND preparer_image_status = 0;

ALTER TABLE scheduled_instance_status DROP COLUMN preparer_status;

-- The secondary schema is now defined independently of the primary's and
-- contains only worker-local tables. Earlier releases applied the full shared
-- schema on every node, so existing worker databases carry empty primary-side
-- tables. Sweep them: none was ever written by worker code, and dropping them
-- makes the on-disk file match what a worker actually is. All idempotent.
DROP TABLE IF EXISTS spaces;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS public_keys;
DROP TABLE IF EXISTS agent_sessions;
DROP TABLE IF EXISTS system_config_revisions;
DROP TABLE IF EXISTS enrollment_requests;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS node_statuses;
DROP TABLE IF EXISTS deployment_configs;
DROP TABLE IF EXISTS deployment_config_history;
DROP TABLE IF EXISTS scheduled_instances;
DROP TABLE IF EXISTS asset_directories;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS asset_versions;
DROP TABLE IF EXISTS asset_migrations;
DROP TABLE IF EXISTS value_directories;
DROP TABLE IF EXISTS configs;
DROP TABLE IF EXISTS config_versions;
DROP TABLE IF EXISTS secret_keyslots;
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS secret_versions;
DROP TABLE IF EXISTS system_secrets;

-- The per-deployment status index is a primary-side display concern, and
-- worker reads are covered by the primary key.
DROP INDEX IF EXISTS idx_scheduled_instance_status_deployment;
