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

-- v0.0.614: seal ids. The secret AEAD binds (secret_id, seal_id) instead of
-- (secret_id, value_version). Legacy rows get seal_id 'v<value_version>',
-- which reproduces their existing associated data byte for byte, so no
-- ciphertext is re-sealed. New value writes issue opaque 'k'-prefixed ids.
ALTER TABLE secret_event_log ADD COLUMN seal_id TEXT NOT NULL DEFAULT '';
UPDATE secret_event_log SET seal_id = 'v' || value_version WHERE seal_id = '';

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

-- v0.0.614: users.last_login_at is gone. It duplicated the newest
-- personal_sessions.created_at for the user. Duplicate passkey credential
-- entries inside users.data_blob (one appended per login) are collapsed by
-- users.MigrateDuplicateCredentials at startup.
ALTER TABLE users DROP COLUMN last_login_at;
