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
-- v0.0.553 rollout, the scheduled_instances deployment_version column
-- add with its Go-side backfill (v0.0.554) after the v0.0.555 rollout, and
-- the merge of scheduled_instances + scheduled_instance_versions into
-- scheduled_instance_event_log with the legacy table drops (v0.0.559) after
-- the v0.0.559 rollout.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

-- Merge nodes + node_versions into node_event_log (created by the schema pass
-- just before this runs). Current name/identifier and enrolled_at denormalise
-- onto every event row (renames were in-place UPDATEs and the accept boundary
-- is not reconstructable) and created_time is the identity's created_at. The
-- NOT EXISTS guard makes the copy single-shot even if a crash lands between
-- it and the drops below. Fresh installs and already-migrated databases fail
-- the copy with "no such table", which ApplyMigrations tolerates.
INSERT INTO node_event_log (
    global_seq, event_time, created_time, author, node_id, version,
    name, identifier, enrolled_time, status, roles, addresses,
    wg_public_key, allowed_spaces, event_type)
SELECT v.global_seq, v.created_at, n.created_at, v.author, v.node_id, v.version,
       n.name, n.identifier, n.enrolled_at, v.status, v.roles, v.addresses,
       v.wg_public_key, v.allowed_spaces,
       CASE WHEN v.version = 1 THEN 1 ELSE 2 END
FROM node_versions v
JOIN nodes n ON n.id = v.node_id
WHERE NOT EXISTS (SELECT 1 FROM node_event_log)
ORDER BY v.node_id, v.version;

-- Merge network_policies + network_policy_versions into
-- network_policy_event_log, same guard shape as nodes above.
INSERT INTO network_policy_event_log (
    global_seq, event_time, created_time, author, policy_id, version,
    data_blob, event_type)
SELECT v.global_seq, v.created_at,
       (SELECT MIN(f.created_at) FROM network_policy_versions f WHERE f.policy_id = v.policy_id),
       v.author, v.policy_id, v.version, v.data_blob,
       CASE WHEN v.version = 1 THEN 1 ELSE 2 END
FROM network_policy_versions v
WHERE NOT EXISTS (SELECT 1 FROM network_policy_event_log)
ORDER BY v.policy_id, v.version;

-- Soft-deleted policies get a synthetic terminal delete event carrying the
-- last content blob (deployment-tombstone shape). Its own guard keeps a
-- crash-window re-run from appending a second delete.
INSERT INTO network_policy_event_log (
    global_seq, event_time, created_time, author, policy_id, version,
    data_blob, event_type)
SELECT 0, p.deleted_at,
       (SELECT MIN(f.created_at) FROM network_policy_versions f WHERE f.policy_id = p.id),
       0, p.id,
       (SELECT MAX(e.version) FROM network_policy_event_log e WHERE e.policy_id = p.id) + 1,
       (SELECT f.data_blob FROM network_policy_versions f WHERE f.policy_id = p.id ORDER BY f.version DESC LIMIT 1),
       3
FROM network_policies p
WHERE p.deleted_at != 0
  AND EXISTS (SELECT 1 FROM network_policy_versions f WHERE f.policy_id = p.id)
  AND NOT EXISTS (SELECT 1 FROM network_policy_event_log e WHERE e.policy_id = p.id AND e.event_type = 3);

-- Drop the legacy tables. The secret/config/asset copies ran as the Go-side
-- one-time migration just before this file (see pq/migrate_values.go).
DROP TABLE nodes;

DROP TABLE node_versions;

DROP TABLE network_policies;

DROP TABLE network_policy_versions;

DROP TABLE secrets;

DROP TABLE secret_spaces;

DROP TABLE secret_versions;

DROP TABLE configs;

DROP TABLE config_spaces;

DROP TABLE config_versions;

DROP TABLE assets;

DROP TABLE asset_spaces;

DROP TABLE asset_versions;
