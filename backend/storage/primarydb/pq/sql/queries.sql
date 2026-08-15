-- === deployment_configs ===

-- CreateDeploymentConfig inserts a new independent deployment and
-- auto-allocates its deployment_id. Deleted deployments do not reserve their
-- former identity tuple.
-- name: CreateDeploymentConfig :one
INSERT INTO deployment_configs (node_id, space_id, name, created_at, version, updated_at, updated_by, spec_blob, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING deployment_id, created_at;

-- name: GetDeploymentConfig :one
SELECT deployment_id, node_id, space_id, name, created_at, version, updated_at, updated_by,
       spec_blob, deleted
FROM deployment_configs
WHERE deployment_id = ?;

-- name: UpsertDeploymentConfig :exec
INSERT INTO deployment_configs (deployment_id, node_id, space_id, name, created_at, version, updated_at, updated_by, spec_blob, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    node_id = excluded.node_id,
    space_id = excluded.space_id,
    name = excluded.name,
    created_at = excluded.created_at,
    version = excluded.version,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    spec_blob = excluded.spec_blob,
    deleted = excluded.deleted;

-- name: ListAllDeploymentConfigs :many
SELECT deployment_id, node_id, space_id, name, created_at, version, updated_at, updated_by,
       spec_blob, deleted
FROM deployment_configs
;

-- === spaces ===

-- name: ListSpaces :many
SELECT id, name FROM spaces ORDER BY id;

-- name: CreateSpace :one
INSERT INTO spaces (name) VALUES (?)
RETURNING id, name;

-- name: UpdateSpace :one
UPDATE spaces SET name = ? WHERE id = ?
RETURNING id, name;

-- name: DeleteSpace :exec
DELETE FROM spaces WHERE id = ?;

-- name: CountDeploymentsForSpace :one
SELECT COUNT(*) FROM deployment_configs WHERE space_id = ? AND deleted = 0;

-- === deployment_config_history ===

-- name: InsertDeploymentConfigHistory :exec
INSERT INTO deployment_config_history (deployment_id, version, updated_at, updated_by, space_id, node_id, spec_blob, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentConfigHistory :many
SELECT deployment_id, version, updated_at, updated_by, space_id, node_id, spec_blob, deleted
FROM deployment_config_history
WHERE deployment_id = ?
ORDER BY version ASC;

-- name: GetDeploymentConfigHistoryVersion :one
SELECT deployment_id, version, updated_at, updated_by, space_id, node_id, spec_blob, deleted
FROM deployment_config_history
WHERE deployment_id = ? AND version = ?;

-- === scheduled_instances ===

-- name: InsertScheduledInstance :one
INSERT INTO scheduled_instances (created_at, deployment_id, deployment_version, node_id, instance_ordinal, state)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, created_at, deployment_id, deployment_version, node_id, instance_ordinal, state;

-- name: GetScheduledInstance :one
SELECT id, created_at, deployment_id, deployment_version, node_id, instance_ordinal, state
FROM scheduled_instances
WHERE id = ?;

-- name: ListNonFinalScheduledInstances :many
SELECT id, created_at, deployment_id, deployment_version, node_id, instance_ordinal, state
FROM scheduled_instances
WHERE state != 2
ORDER BY id ASC;

-- name: ListNonFinalScheduledInstancesForDeployment :many
SELECT id, created_at, deployment_id, deployment_version, node_id, instance_ordinal, state
FROM scheduled_instances
WHERE deployment_id = ? AND state != 2
ORDER BY id ASC;

-- name: ListLatestScheduledInstancePerOrdinal :many
-- The newest incarnation of every (deployment, ordinal), whatever its state.
-- Rebuilds the retained view of ordinals whose last instance has been finalized,
-- which is all a stopped deployment has left to show.
SELECT si.id, si.created_at, si.deployment_id, si.deployment_version, si.node_id, si.instance_ordinal, si.state
FROM scheduled_instances si
JOIN (
    SELECT deployment_id, instance_ordinal, MAX(id) AS id
    FROM scheduled_instances
    GROUP BY deployment_id, instance_ordinal
) latest ON latest.id = si.id
ORDER BY si.id ASC;

-- name: UpdateScheduledInstanceState :exec
UPDATE scheduled_instances SET state = ? WHERE id = ?;

-- === scheduled_instance_status ===

-- name: InsertScheduledInstanceStatus :exec
INSERT INTO scheduled_instance_status (
    scheduled_instance_id, updated_at, deployment_id,
    preparer_config_version, preparer_artifact,
    preparer_inputs_status, preparer_image_status,
    runner_config_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at, runner_extra_blob
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scheduled_instance_id, updated_at) DO UPDATE SET
    deployment_id = excluded.deployment_id,
    preparer_config_version = excluded.preparer_config_version,
    preparer_artifact = excluded.preparer_artifact,
    preparer_inputs_status = excluded.preparer_inputs_status,
    preparer_image_status = excluded.preparer_image_status,
    runner_config_version = excluded.runner_config_version,
    runner_pid = excluded.runner_pid,
    runner_artifact = excluded.runner_artifact,
    runner_status = excluded.runner_status,
    runner_num_restarts = excluded.runner_num_restarts,
    runner_last_restart_at = excluded.runner_last_restart_at,
    runner_extra_blob = excluded.runner_extra_blob;

-- name: ListLatestScheduledInstanceStatuses :many
SELECT s.scheduled_instance_id, s.updated_at, s.deployment_id,
       s.preparer_config_version, s.preparer_artifact,
       s.preparer_inputs_status, s.preparer_image_status,
       s.runner_config_version, s.runner_pid, s.runner_artifact, s.runner_status,
       s.runner_num_restarts, s.runner_last_restart_at, s.runner_extra_blob
FROM scheduled_instance_status s
JOIN (
    SELECT scheduled_instance_id, MAX(updated_at) AS updated_at
    FROM scheduled_instance_status
    GROUP BY scheduled_instance_id
) latest ON latest.scheduled_instance_id = s.scheduled_instance_id AND latest.updated_at = s.updated_at;

-- name: ListScheduledInstanceStatusHistorySince :many
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_config_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob
FROM scheduled_instance_status
WHERE scheduled_instance_id = ? AND updated_at > ?
ORDER BY updated_at ASC;

-- name: ListScheduledInstanceStatusHistoryForDeployment :many
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_config_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob
FROM scheduled_instance_status
WHERE deployment_id = ?
ORDER BY updated_at ASC;

-- === users ===

-- name: GetUser :one
SELECT id, name, data_blob, created_at, last_login_at FROM users WHERE id = ?;

-- UpsertUser deliberately leaves created_at and last_login_at alone on
-- conflict: the same call both creates users and rewrites their credential
-- blob later.
-- name: UpsertUser :exec
INSERT INTO users (id, name, data_blob, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob;

-- name: TouchUserLastLogin :exec
UPDATE users SET last_login_at = ? WHERE id = ?;

-- name: ListUsers :many
SELECT id, name, data_blob, created_at, last_login_at FROM users ORDER BY id;

-- === agent_sessions ===

-- name: InsertAgentSession :exec
INSERT INTO agent_sessions (id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
                            status, requesting_address, approval_code, approved_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?);

-- name: GetAgentSession :one
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE id = ?;

-- name: ListAgentSessionsForUser :many
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListPendingAgentSessionsForUser :many
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? AND status = 1 ORDER BY created_at DESC;

-- name: SetAgentSessionStatus :exec
UPDATE agent_sessions SET status = ?, approved_at = ?, revoked_at = ?
WHERE id = ?;

-- ApproveAgentSession records the approver's scopes and the approval together,
-- and only from PENDING, so two operators racing on the same request cannot
-- both approve it and the second one's scopes cannot overwrite the first's.
-- name: ApproveAgentSession :execrows
UPDATE agent_sessions SET status = 2, approved_at = ?, scopes = ?
WHERE id = ? AND user_id = ? AND status = 1;

-- ClaimAgentSessionToken mints a session's token exactly once. The empty
-- token_hash in the WHERE clause is the guard: two concurrent pickups race here
-- and only the one that finds the row unclaimed reports a row affected, so a
-- session can never hand out two working tokens.
-- name: ClaimAgentSessionToken :execrows
UPDATE agent_sessions SET token_hash = ?, token_prefix = ?, expires_at = ?, scopes = ?
WHERE id = ? AND status = 2 AND length(token_hash) = 0;

-- name: RevokeAgentSession :exec
UPDATE agent_sessions SET revoked_at = ?, status = ?
WHERE id = ? AND user_id = ? AND revoked_at = 0;

-- === public_keys ===

-- name: GetPublicKey :one
SELECT kid, key_bytes FROM public_keys WHERE kid = ?;

-- name: UpsertPublicKey :exec
INSERT INTO public_keys (kid, key_bytes) VALUES (?, ?)
ON CONFLICT(kid) DO UPDATE SET key_bytes = excluded.key_bytes;

-- === configs ===

-- name: ListConfigRows :many
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM configs
ORDER BY name;

-- name: GetConfigRowByID :one
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM configs WHERE id = ?;

-- name: GetConfigInDirectoryByName :one
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM configs WHERE space_id = ? AND value_directory_id = ? AND name = ?;

-- name: InsertConfigRow :one
INSERT INTO configs (name, space_id, value_directory_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, space_id, value_directory_id, created_at, created_by;

-- name: RenameConfigRow :exec
UPDATE configs SET name = ? WHERE id = ?;

-- name: DeleteConfigRow :exec
DELETE FROM configs WHERE id = ?;

-- name: ListConfigVersionRows :many
SELECT id, config_id, version, value, created_at, created_by
FROM config_versions
ORDER BY config_id, version;

-- name: ListConfigVersionsByConfigID :many
SELECT id, config_id, version, value, created_at, created_by
FROM config_versions WHERE config_id = ?
ORDER BY version ASC;

-- name: GetConfigVersionByID :one
SELECT v.id, v.config_id, v.version, v.value, v.created_at, v.created_by, c.name, c.space_id
FROM config_versions v
JOIN configs c ON c.id = v.config_id
WHERE v.id = ?;

-- name: GetNextConfigVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM config_versions
WHERE config_id = ?;

-- name: InsertConfigVersion :one
INSERT INTO config_versions (config_id, version, value, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, config_id, version, value, created_at, created_by;

-- name: DeleteConfigVersionsByConfigID :exec
DELETE FROM config_versions WHERE config_id = ?;

-- === value directories (shared by secrets and configs) ===

-- name: CountValueDirectorySiblingsWithName :one
SELECT COUNT(*) FROM value_directories
WHERE space_id = ? AND parent_id = ? AND name = ? AND id != ?;

-- name: GetValueDirectoryByID :one
SELECT id, space_id, name, parent_id, created_at, created_by
FROM value_directories
WHERE id = ?;

-- name: ListValueDirectories :many
SELECT id, space_id, name, parent_id, created_at, created_by
FROM value_directories
ORDER BY space_id, parent_id, name;

-- name: SetValueDirectoryName :exec
UPDATE value_directories SET name = ? WHERE id = ?;

-- name: InsertValueDirectory :one
INSERT INTO value_directories (space_id, name, parent_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, name, parent_id, created_at, created_by;

-- name: SetValueDirectoryParent :exec
UPDATE value_directories SET parent_id = ? WHERE id = ?;

-- name: DeleteValueDirectory :exec
DELETE FROM value_directories WHERE id = ?;

-- name: CountChildValueDirectories :one
SELECT COUNT(*) FROM value_directories WHERE parent_id = ?;

-- name: CountSecretsInDirectory :one
SELECT COUNT(*) FROM secrets WHERE value_directory_id = ?;

-- name: CountConfigsInDirectory :one
SELECT COUNT(*) FROM configs WHERE value_directory_id = ?;

-- name: SetSecretValueDirectoryID :exec
UPDATE secrets SET value_directory_id = ? WHERE id = ?;

-- name: SetConfigValueDirectoryID :exec
UPDATE configs SET value_directory_id = ? WHERE id = ?;

-- name: SetSecretSpace :exec
UPDATE secrets SET space_id = ?, value_directory_id = ? WHERE id = ?;

-- name: SetConfigSpace :exec
UPDATE configs SET space_id = ?, value_directory_id = ? WHERE id = ?;

-- name: CountConfigSiblingsWithName :one
SELECT COUNT(*) FROM configs
WHERE space_id = ? AND value_directory_id = ? AND name = ? AND id != ?;

-- name: CountSecretSiblingsWithName :one
SELECT COUNT(*) FROM secrets
WHERE space_id = ? AND value_directory_id = ? AND name = ? AND id != ?;

-- === assets ===

-- name: ListAssetRows :many
SELECT id, space_id, key, asset_directory_id, created_at, created_by
FROM assets
ORDER BY key;

-- name: GetAssetByID :one
SELECT id, space_id, key, asset_directory_id, created_at, created_by
FROM assets
WHERE id = ?;

-- name: GetAssetInDirectoryByKey :one
SELECT id, space_id, key, asset_directory_id, created_at, created_by
FROM assets
WHERE space_id = ? AND asset_directory_id = ? AND key = ?;

-- name: CountAssetSiblingsWithKey :one
SELECT COUNT(*) FROM assets
WHERE space_id = ? AND asset_directory_id = ? AND key = ? AND id != ?;

-- name: CountDirectorySiblingsWithKey :one
SELECT COUNT(*) FROM asset_directories
WHERE space_id = ? AND parent_id = ? AND key = ? AND id != ?;

-- name: InsertAssetRow :one
INSERT INTO assets (space_id, key, asset_directory_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, key, asset_directory_id, created_at, created_by;

-- name: RenameAssetKey :exec
UPDATE assets SET key = ? WHERE id = ?;

-- name: SetAssetDirectoryID :exec
UPDATE assets SET asset_directory_id = ? WHERE id = ?;

-- name: SetAssetSpace :exec
UPDATE assets SET space_id = ?, asset_directory_id = ? WHERE id = ?;

-- name: DeleteAssetRow :exec
DELETE FROM assets WHERE id = ?;

-- name: GetAssetDirectoryByID :one
SELECT id, space_id, key, parent_id, created_at, created_by
FROM asset_directories
WHERE id = ?;

-- name: ListAssetDirectories :many
SELECT id, space_id, key, parent_id, created_at, created_by
FROM asset_directories
ORDER BY space_id, parent_id, key;

-- name: SetAssetDirectoryKey :exec
UPDATE asset_directories SET key = ? WHERE id = ?;

-- name: InsertAssetDirectory :one
INSERT INTO asset_directories (space_id, key, parent_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, key, parent_id, created_at, created_by;

-- name: SetAssetDirectoryParent :exec
UPDATE asset_directories SET parent_id = ? WHERE id = ?;

-- name: DeleteAssetDirectory :exec
DELETE FROM asset_directories WHERE id = ?;

-- name: CountAssetsInDirectory :one
SELECT COUNT(*) FROM assets WHERE asset_directory_id = ?;

-- name: CountChildAssetDirectories :one
SELECT COUNT(*) FROM asset_directories WHERE parent_id = ?;

-- name: ListPublishedAssetVersionMetas :many
SELECT asset_id, id, version, created_at, created_by, size_bytes, location
FROM asset_versions
WHERE location NOT LIKE 'pending://%'
ORDER BY asset_id, version DESC;

-- name: GetLatestAssetVersion :one
SELECT id, asset_id, version, created_at, created_by, location, size_bytes, blob
FROM asset_versions
WHERE asset_id = ? AND location NOT LIKE 'pending://%'
ORDER BY version DESC
LIMIT 1;

-- name: GetAssetVersionByNumber :one
SELECT id, asset_id, version, created_at, created_by, location, size_bytes, blob
FROM asset_versions
WHERE asset_id = ? AND version = ? AND location NOT LIKE 'pending://%';

-- name: ListAssetVersions :many
SELECT id, asset_id, version, created_at, created_by, location, size_bytes, blob
FROM asset_versions
WHERE asset_id = ? AND location NOT LIKE 'pending://%'
ORDER BY version ASC;

-- name: ListAssetVersionsIncludingPending :many
SELECT id, asset_id, version, created_at, created_by, location, size_bytes, blob
FROM asset_versions
WHERE asset_id = ?
ORDER BY version ASC;

-- name: GetNextAssetVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM asset_versions
WHERE asset_id = ?;

-- name: InsertAssetVersion :one
INSERT INTO asset_versions (asset_id, version, created_at, created_by, location, size_bytes, blob)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, asset_id, version, created_at, created_by, location, size_bytes, blob;

-- name: UpdateAssetVersionLocation :one
UPDATE asset_versions
SET location = ?
WHERE id = ?
RETURNING id, asset_id, version, created_at, created_by, location, size_bytes, blob;

-- name: DeleteAssetVersionByID :exec
DELETE FROM asset_versions WHERE id = ?;

-- name: DeleteAssetVersionsByAssetID :exec
DELETE FROM asset_versions WHERE asset_id = ?;

-- === asset_migrations ===

-- name: GetUnfinishedAssetMigration :one
SELECT id, old_config_version_id, new_config_version_id, status, last_error,
       created_at, started_at, last_attempt_at, finished_at
FROM asset_migrations
WHERE status != 'finished'
ORDER BY id
LIMIT 1;

-- name: InsertAssetMigration :one
INSERT INTO asset_migrations (
    old_config_version_id, new_config_version_id, status, created_at
) VALUES (?, ?, 'pending', ?)
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: StartAssetMigration :one
UPDATE asset_migrations
SET status = 'running', started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END,
    last_attempt_at = ?, last_error = ''
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: RecordAssetMigrationError :one
UPDATE asset_migrations
SET status = 'running', last_attempt_at = ?, last_error = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: FinishAssetMigration :one
UPDATE asset_migrations
SET status = 'finished', last_error = '', finished_at = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- === secret_keyslots ===

-- name: ListSecretKeyslots :many
SELECT slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at
FROM secret_keyslots ORDER BY slot;

-- name: UpsertSecretKeyslot :exec
INSERT INTO secret_keyslots (slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(slot) DO UPDATE SET
    smk_version = excluded.smk_version,
    wrapped_smk = excluded.wrapped_smk,
    nonce = excluded.nonce,
    kdf_salt = excluded.kdf_salt,
    created_at = excluded.created_at;

-- === secrets ===

-- name: ListSecretRows :many
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM secrets
ORDER BY name;

-- name: GetSecretRowByID :one
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM secrets WHERE id = ?;

-- name: GetSecretInDirectoryByName :one
SELECT id, name, space_id, value_directory_id, created_at, created_by
FROM secrets WHERE space_id = ? AND value_directory_id = ? AND name = ?;

-- name: InsertSecretRow :one
INSERT INTO secrets (name, space_id, value_directory_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, space_id, value_directory_id, created_at, created_by;

-- name: RenameSecretRow :exec
UPDATE secrets SET name = ? WHERE id = ?;

-- name: DeleteSecretRow :exec
DELETE FROM secrets WHERE id = ?;

-- name: ListSecretVersionRecords :many
SELECT v.id, v.secret_id, v.version, v.smk_version, v.ciphertext, v.nonce, v.created_at, v.created_by,
       s.name, s.space_id
FROM secret_versions v
JOIN secrets s ON s.id = v.secret_id
ORDER BY v.secret_id, v.version;

-- name: ListSecretVersionMetas :many
SELECT id, secret_id, version, created_at, created_by
FROM secret_versions
ORDER BY secret_id, version;

-- name: GetNextSecretVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM secret_versions
WHERE secret_id = ?;

-- name: InsertSecretVersion :one
INSERT INTO secret_versions (secret_id, version, smk_version, ciphertext, nonce, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, secret_id, version, smk_version, ciphertext, nonce, created_at, created_by;

-- name: UpdateSecretVersionCiphertext :exec
UPDATE secret_versions SET smk_version = ?, ciphertext = ?, nonce = ? WHERE id = ?;

-- name: DeleteSecretVersionsBySecretID :exec
DELETE FROM secret_versions WHERE secret_id = ?;

-- name: ListSecretVersionIDsBySecretID :many
SELECT id FROM secret_versions WHERE secret_id = ? ORDER BY version;

-- name: ListSecretVersionsBySecretID :many
SELECT id, secret_id, version, created_at, created_by
FROM secret_versions WHERE secret_id = ?
ORDER BY version ASC;

-- name: ListConfigVersionIDsByConfigID :many
SELECT id FROM config_versions WHERE config_id = ? ORDER BY version;

-- === system_secrets ===

-- name: GetSystemSecret :one
SELECT name, smk_version, ciphertext, nonce, created_at, updated_at
FROM system_secrets
WHERE name = ?;

-- name: UpsertSystemSecret :exec
INSERT INTO system_secrets (name, smk_version, ciphertext, nonce, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    smk_version = excluded.smk_version,
    ciphertext = excluded.ciphertext,
    nonce = excluded.nonce,
    updated_at = excluded.updated_at;


-- name: GetLatestConfig :one
select * from system_config_revisions order by id desc limit 1;

-- name: GetConfigByID :one
SELECT id, updated_at, config_blob FROM system_config_revisions WHERE id = ?;

-- === local_kv ===

-- name: GetLocalKV :one
SELECT value FROM local_kv WHERE key = ?;

-- name: UpsertLocalKV :exec
INSERT INTO local_kv (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;


-- === authz ===

-- name: ListAuthzRuleTemplateRows :many
SELECT id, name, builtin, deleted, created_by, created_at, data_blob FROM authz_rule_templates;

-- name: InsertAuthzRuleTemplateRow :one
INSERT INTO authz_rule_templates (name, builtin, deleted, created_by, created_at, data_blob)
VALUES (?, 0, 0, ?, ?, ?) RETURNING id;

-- name: UpdateAuthzRuleTemplateRow :exec
UPDATE authz_rule_templates SET name = ?, deleted = ?, data_blob = ? WHERE id = ?;

-- name: UpsertAuthzRuleTemplateRow :exec
INSERT INTO authz_rule_templates (id, name, builtin, deleted, created_by, created_at, data_blob)
VALUES (?, ?, 1, 0, 0, 0, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    builtin = 1,
    deleted = 0,
    data_blob = excluded.data_blob;

-- name: ListAuthzGrantRows :many
SELECT id, user_id, template_id, created_by, created_at, data_blob FROM authz_grants;

-- name: InsertAuthzGrantRow :one
INSERT INTO authz_grants (user_id, template_id, created_by, created_at, data_blob)
VALUES (?, ?, ?, ?, ?) RETURNING id;

-- name: DeleteAuthzGrantRow :exec
DELETE FROM authz_grants WHERE id = ?;

-- name: ListGlobalAccessRuleRows :many
SELECT id, name, created_by, created_at, data_blob FROM global_access_rules;

-- name: InsertGlobalAccessRuleRow :one
INSERT INTO global_access_rules (name, created_by, created_at, data_blob)
VALUES (?, ?, ?, ?) RETURNING id;

-- name: DeleteGlobalAccessRuleRow :exec
DELETE FROM global_access_rules WHERE id = ?;

-- === events ===

-- name: InsertEvent :one
INSERT INTO events (ts, author_id, entity_type, entity_id, action, blob)
VALUES (?, ?, ?, ?, ?, ?) RETURNING id;

-- name: MaxEventEntityID :one
SELECT CAST(COALESCE(MAX(entity_id), 0) AS INTEGER) FROM events WHERE entity_type = ?;

-- name: GetEventByID :one
SELECT id, ts, author_id, entity_type, entity_id, action, blob
FROM events WHERE id = ?;

-- name: ListEventsByEntity :many
SELECT id, ts, author_id, entity_type, entity_id, action, blob
FROM events WHERE entity_type = ? AND entity_id = ?
ORDER BY id ASC;

-- name: ListEventsByType :many
SELECT id, ts, author_id, entity_type, entity_id, action, blob
FROM events WHERE entity_type = ?
ORDER BY entity_id, id ASC;

-- === config displays ===

-- name: ListConfigDisplays :many
SELECT id, space_id, name, directory_id, updated_at, updated_by
FROM config_displays ORDER BY name;

-- name: GetConfigDisplayByID :one
SELECT id, space_id, name, directory_id, updated_at, updated_by
FROM config_displays WHERE id = ?;

-- name: GetConfigDisplayByName :one
SELECT id, space_id, name, directory_id, updated_at, updated_by
FROM config_displays WHERE space_id = ? AND directory_id = ? AND name = ?;

-- name: InsertConfigDisplay :exec
INSERT INTO config_displays (id, space_id, name, directory_id, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?);

-- name: RenameConfigDisplay :exec
UPDATE config_displays SET name = ?, updated_at = ?, updated_by = ? WHERE id = ?;

-- name: SetConfigDisplayDirectory :exec
UPDATE config_displays SET directory_id = ?, updated_at = ?, updated_by = ? WHERE id = ?;

-- name: SetConfigDisplaySpace :exec
UPDATE config_displays SET space_id = ?, directory_id = ?, updated_at = ?, updated_by = ? WHERE id = ?;

-- name: DeleteConfigDisplay :exec
DELETE FROM config_displays WHERE id = ?;

-- name: CountConfigDisplaysInDirectory :one
SELECT COUNT(*) FROM config_displays WHERE directory_id = ?;

-- name: CountConfigDisplaySiblingsWithName :one
SELECT COUNT(*) FROM config_displays
WHERE space_id = ? AND directory_id = ? AND name = ? AND id != ?;

-- name: UpdateDeploymentSpecBlobInPlace :exec
UPDATE deployment_configs SET spec_blob = ? WHERE deployment_id = ?;
