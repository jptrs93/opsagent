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
-- add with its Go-side backfill (v0.0.554) after the v0.0.555 rollout,
-- the merge of scheduled_instances + scheduled_instance_versions into
-- scheduled_instance_event_log with the legacy table drops (v0.0.559) after
-- the v0.0.559 rollout, and the event log consolidation of secrets, configs,
-- assets, network policies and nodes (v0.0.563: Go-side value-entity copies
-- in pq/migrate_values.go, node/policy SQL copies with synthetic policy
-- delete events, and the 13 legacy table drops) after the v0.0.563 rollout,
-- and the authz event log merge (v0.0.566: template/grant/global-rule copies
-- with synthetic delete events and the 4 legacy table drops) after the
-- v0.0.566 rollout, and the event-log changed-flag rebuild (v0.0.569:
-- *_changed columns plus NOT NULL carried-forward payloads on the
-- deployment/asset/secret/config logs, a Go shape migration in
-- pq/migrate_event_flags.go) after the v0.0.570 rollout.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

ALTER TABLE node_statuses ADD COLUMN host_addresses TEXT NOT NULL DEFAULT '[]';

-- Version node-reported facts and use a clock for observed state.
ALTER TABLE node_event_log ADD COLUMN host_addresses TEXT NOT NULL DEFAULT '[]';
ALTER TABLE node_event_log ADD COLUMN enrollment_requested_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_statuses ADD COLUMN observed_at INTEGER NOT NULL DEFAULT 0;

-- Preserve the last legacy observation once, without overwriting newer history.
INSERT INTO node_status_log (node_id, updated_at, last_connected_at, is_connected, opendeploy_version, remote_address)
SELECT old.node_id, MAX(old.observed_at * 1000000, old.last_connected_at * 1000000, 1),
       old.last_connected_at, old.is_connected, old.opendeploy_version, old.remote_address
FROM node_statuses old
WHERE (old.observed_at != 0 OR old.last_connected_at != 0 OR old.is_connected != 0 OR old.opendeploy_version != '' OR old.remote_address != '')
  AND NOT EXISTS (SELECT 1 FROM node_status_log current WHERE current.node_id = old.node_id);

-- Every commit that writes an observation now consumes the global sequence.
ALTER TABLE scheduled_instance_status ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_status_log ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
