-- Add migrations here. Statements re-run on every startup, so each must be
-- idempotent (sqlitedb.ApplyMigrations tolerates "already applied" errors:
-- duplicate column, no such column, no such table). Comments must not contain
-- semicolons — statements are split on them.
--
-- History note: all migrations accumulated up to v0.0.541 (2026-08-31),
-- ending with the node_versions split and the merge of enrollment_requests
-- into nodes, were removed after every active cluster had been rolled
-- forward. The spec-version column renames and the one-time deployment event
-- log migration (v0.0.549) were removed likewise after the v0.0.550 rollout,
-- the legacy deployment table drops plus the event_time rename and
-- created_time backfill (v0.0.553 Deployment/DeploymentDef split) after the
-- v0.0.553 rollout, and the scheduled_instances deployment_version column
-- add with its Go-side backfill (v0.0.554) after the v0.0.555 rollout.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

-- Merge scheduled_instances + scheduled_instance_versions into
-- scheduled_instance_event_log (created by the schema pass just before this
-- runs). Identity columns denormalise onto every version row and created_time
-- is the first version's created_at. The NOT EXISTS guard makes the copy
-- single-shot even if a crash lands between it and the drops below. Fresh
-- installs and already-migrated databases fail the copy with "no such table",
-- which ApplyMigrations tolerates.
INSERT INTO scheduled_instance_event_log (
    scheduled_instance_id, version, global_seq, event_time, created_time,
    deployment_id, deployment_version, deployment_spec_version,
    node_id, instance_ordinal, space_id, state)
SELECT v.scheduled_instance_id, v.version, v.global_seq, v.created_at,
       (SELECT f.created_at FROM scheduled_instance_versions f
        WHERE f.scheduled_instance_id = v.scheduled_instance_id ORDER BY f.id LIMIT 1),
       si.deployment_id, si.deployment_version, si.deployment_spec_version,
       si.node_id, si.instance_ordinal, si.space_id, v.state
FROM scheduled_instance_versions v
JOIN scheduled_instances si ON si.id = v.scheduled_instance_id
WHERE NOT EXISTS (SELECT 1 FROM scheduled_instance_event_log)
ORDER BY v.id;

DROP TABLE scheduled_instances;

DROP TABLE scheduled_instance_versions;
