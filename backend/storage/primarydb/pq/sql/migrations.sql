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
-- pq/migrate_event_flags.go) after the v0.0.570 rollout, and the node
-- observation move (v0.0.587: node_event_log host_addresses and
-- enrollment_requested_at columns, the one-time copy of the last legacy
-- node_statuses row into node_status_log, and the global_seq columns on the
-- two observed status logs) after the v0.0.587 rollout, and the deployment
-- scheduling facet (v0.0.611: scheduling_version and scheduling_changed
-- columns on deployment_event_log plus the Go shape migration in
-- pq/migrate_scheduling.go that lifted node_id and ContainerSpec.running
-- into Deployment.scheduling) after the v0.0.611 rollout. A database from
-- before v0.0.611 fails at startup on the missing columns rather than
-- opening with every deployment read as stopped.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

DROP TABLE IF EXISTS node_statuses;

-- v0.0.613: drop the never-read space facet version from the value entity
-- event logs. Only deployments keep a space_version.
ALTER TABLE secret_event_log DROP COLUMN space_version;
ALTER TABLE secret_event_log DROP COLUMN space_changed;
ALTER TABLE config_event_log DROP COLUMN space_version;
ALTER TABLE config_event_log DROP COLUMN space_changed;
ALTER TABLE asset_event_log DROP COLUMN space_version;
ALTER TABLE asset_event_log DROP COLUMN space_changed;

-- v0.0.614: the secret AEAD binds secret_id alone. Rows sealed under the
-- earlier (secret_id, value_version) binding, rows from unreleased v0.0.614
-- builds that carried a seal_id column, and the system_secrets table are
-- re-sealed and folded into secret_event_log (space 0) by the secrets
-- manager at the first unlocked start, which then drops the column and the
-- table (secrets.Manager.migrateSealsLocked). Nothing to do here.

-- v0.0.614: asset content identity on the event log. Every asset event row
-- carries the storage key of its content (the asset_store row id that names
-- the local file and the S3 object). Inline blobs are gone: content of every
-- size lives in the large-asset root and S3, and assets.MigrateInlineContent
-- writes existing inline blobs out at startup and then drops the column.
-- asset_migrations is gone: the reconciler converges from the placement flags
-- and never needed the row. (No semicolons in this comment: the runner splits
-- on them.)
ALTER TABLE asset_event_log ADD COLUMN storage_key TEXT NOT NULL DEFAULT '';
UPDATE asset_event_log SET storage_key = COALESCE((SELECT s.id FROM asset_store s WHERE s.sha256 = asset_event_log.sha256), '')
 WHERE storage_key = '' AND sha256 != '';
DROP TABLE IF EXISTS asset_migrations;

-- v0.0.614: bearer tokens are opaque (u_<id>.<secret>, a_<id>.<secret>) and
-- verified by session row and hash, so the JWT verification keys are gone.
-- personal_sessions became user_sessions. The old rows are not
-- carried over: their JWT-derived hashes can never match a new-format token.
DROP TABLE IF EXISTS public_keys;
DROP TABLE IF EXISTS personal_sessions;

-- v0.0.614: users, spaces, value_directories, asset_directories,
-- system_config_revisions, nix_store_resets, agent_sessions, user_sessions,
-- and secret_keyslots became the append-only *_event_log tables (one row per
-- event, global_seq, event_time, author, event_type).
-- pq.renameLegacyEventTables and pq.copyLegacyEventTables rebuild each legacy
-- table at startup, copying every row as a seq-0 create event. Nothing to do
-- here.

-- v0.0.614: tables no release has read for a long time, left behind in
-- primaries installed before their schema was removed: the pre-event-log
-- system_config and secret_config_directories, the code-completion events
-- table, config_displays, and two secondary tables from when the primary and
-- secondary shared one schema file. Nothing reads them, so no data moves.
DROP TABLE IF EXISTS system_config;
DROP TABLE IF EXISTS secret_config_directories;
DROP TABLE IF EXISTS config_displays;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS local_runtime_inputs;
DROP TABLE IF EXISTS local_scheduled_instance_cache;

-- v0.0.615: the observed status logs carry the wall-clock event time of the
-- commit that recorded them, so a status row replays on the event stream with
-- the same time as the authored rows of its commit. Existing rows take the
-- producer clock (updated_at is HLC nanoseconds).
ALTER TABLE scheduled_instance_status ADD COLUMN event_time INTEGER NOT NULL DEFAULT 0;
UPDATE scheduled_instance_status SET event_time = updated_at / 1000000 WHERE event_time = 0;
ALTER TABLE node_status_log ADD COLUMN event_time INTEGER NOT NULL DEFAULT 0;
UPDATE node_status_log SET event_time = updated_at / 1000000 WHERE event_time = 0;
