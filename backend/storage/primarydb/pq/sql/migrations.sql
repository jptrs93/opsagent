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
-- delete events, and the 13 legacy table drops) after the v0.0.563 rollout.
-- Upgrading a database from before then requires stepping through a release
-- that still carried them. Databases migrated through v0.0.541 keep a dead
-- NULL-only nodes.enrollment_id column: its UNIQUE constraint blocks
-- ALTER TABLE DROP COLUMN, and no query references it.

-- Merge authz_rule_templates + authz_rule_template_versions into
-- authz_rule_template_event_log (created by the schema pass just before this
-- runs). Current name/builtin denormalise onto every event row (renames were
-- in-place UPDATEs) and created_time is the v1 row's created_at. The
-- NOT EXISTS guard makes the copy single-shot even if a crash lands between
-- it and the drops below. Fresh installs and already-migrated databases fail
-- the copy with "no such table", which ApplyMigrations tolerates.
INSERT INTO authz_rule_template_event_log (
    global_seq, event_time, created_time, author, template_id, version,
    name, builtin, data_blob, event_type)
SELECT v.global_seq, v.created_at,
       (SELECT MIN(f.created_at) FROM authz_rule_template_versions f WHERE f.template_id = v.template_id),
       v.author, v.template_id, v.version, t.name, t.builtin, v.data_blob,
       CASE WHEN v.version = 1 THEN 1 ELSE 2 END
FROM authz_rule_template_versions v
JOIN authz_rule_templates t ON t.id = v.template_id
WHERE NOT EXISTS (SELECT 1 FROM authz_rule_template_event_log)
ORDER BY v.template_id, v.version;

-- Soft-deleted templates get a synthetic terminal delete event carrying the
-- last content blob (deployment-tombstone shape). Its own guard keeps a
-- crash-window re-run from appending a second delete.
INSERT INTO authz_rule_template_event_log (
    global_seq, event_time, created_time, author, template_id, version,
    name, builtin, data_blob, event_type)
SELECT 0, t.deleted_at,
       (SELECT MIN(f.created_at) FROM authz_rule_template_versions f WHERE f.template_id = t.id),
       0, t.id,
       (SELECT MAX(e.version) FROM authz_rule_template_event_log e WHERE e.template_id = t.id) + 1,
       t.name, t.builtin,
       (SELECT f.data_blob FROM authz_rule_template_versions f WHERE f.template_id = t.id ORDER BY f.version DESC LIMIT 1),
       3
FROM authz_rule_templates t
WHERE t.deleted_at != 0
  AND EXISTS (SELECT 1 FROM authz_rule_template_versions f WHERE f.template_id = t.id)
  AND NOT EXISTS (SELECT 1 FROM authz_rule_template_event_log e WHERE e.template_id = t.id AND e.event_type = 3);

-- Copy authz_grants into authz_grant_event_log, same guard shape as templates
-- above. Legacy grant rows carried no global_seq.
INSERT INTO authz_grant_event_log (
    global_seq, event_time, created_time, author, grant_id, version,
    user_id, template_id, data_blob, event_type)
SELECT 0, g.created_at, g.created_at, g.author, g.id, 1,
       g.user_id, g.template_id, g.data_blob, 1
FROM authz_grants g
WHERE NOT EXISTS (SELECT 1 FROM authz_grant_event_log)
ORDER BY g.id;

INSERT INTO authz_grant_event_log (
    global_seq, event_time, created_time, author, grant_id, version,
    user_id, template_id, data_blob, event_type)
SELECT 0, g.deleted_at, g.created_at, 0, g.id, 2,
       g.user_id, g.template_id, g.data_blob, 3
FROM authz_grants g
WHERE g.deleted_at != 0
  AND NOT EXISTS (SELECT 1 FROM authz_grant_event_log e WHERE e.grant_id = g.id AND e.event_type = 3);

-- Copy global_access_rules into global_access_rule_event_log, same shapes.
INSERT INTO global_access_rule_event_log (
    global_seq, event_time, created_time, author, rule_id, version,
    name, data_blob, event_type)
SELECT 0, r.created_at, r.created_at, r.author, r.id, 1,
       r.name, r.data_blob, 1
FROM global_access_rules r
WHERE NOT EXISTS (SELECT 1 FROM global_access_rule_event_log)
ORDER BY r.id;

INSERT INTO global_access_rule_event_log (
    global_seq, event_time, created_time, author, rule_id, version,
    name, data_blob, event_type)
SELECT 0, r.deleted_at, r.created_at, 0, r.id, 2,
       r.name, r.data_blob, 3
FROM global_access_rules r
WHERE r.deleted_at != 0
  AND NOT EXISTS (SELECT 1 FROM global_access_rule_event_log e WHERE e.rule_id = r.id AND e.event_type = 3);

DROP TABLE authz_rule_templates;

DROP TABLE authz_rule_template_versions;

DROP TABLE authz_grants;

DROP TABLE global_access_rules;
