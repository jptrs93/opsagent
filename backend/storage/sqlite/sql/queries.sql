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

-- name: GetConfigHistorySpecBlob :one
SELECT spec_blob
FROM deployment_config_history
WHERE deployment_id = ? AND version = ?;

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

-- name: GetLatestScheduledInstanceStatus :one
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_config_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob
FROM scheduled_instance_status
WHERE scheduled_instance_id = ?
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListScheduledInstanceStatusHistory :many
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_config_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob
FROM scheduled_instance_status
WHERE scheduled_instance_id = ?
ORDER BY updated_at ASC;

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

-- === local_scheduled_instance_cache ===

-- name: UpsertLocalScheduledInstanceCache :exec
INSERT INTO local_scheduled_instance_cache (instance_id, blob)
VALUES (?, ?)
ON CONFLICT(instance_id) DO UPDATE SET blob = excluded.blob;

-- name: DeleteLocalScheduledInstanceCache :exec
DELETE FROM local_scheduled_instance_cache WHERE instance_id = ?;

-- name: ListLocalScheduledInstanceCache :many
SELECT instance_id, blob FROM local_scheduled_instance_cache;

-- === users ===

-- name: GetUser :one
SELECT id, name, data_blob FROM users WHERE id = ?;

-- name: UpsertUser :exec
INSERT INTO users (id, name, data_blob) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob;

-- name: ListUsers :many
SELECT id, name, data_blob FROM users ORDER BY id;

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

-- name: ListPublicKeys :many
SELECT kid, key_bytes FROM public_keys ORDER BY kid;

-- === configs ===

-- name: ListUserConfigs :many
SELECT c.id, c.name, c.version, c.space_id, c.value, c.created_at, c.updated_by
FROM configs c
JOIN (
    SELECT name, MAX(version) AS version
    FROM configs
    GROUP BY name
) latest ON latest.name = c.name AND latest.version = c.version
ORDER BY c.name;

-- name: ListAllUserConfigs :many
SELECT id, name, version, space_id, value, created_at, updated_by
FROM configs ORDER BY name, version;

-- name: GetUserConfig :one
SELECT id, name, version, space_id, value, created_at, updated_by
FROM configs WHERE name = ?
ORDER BY version DESC
LIMIT 1;

-- name: GetUserConfigVersion :one
SELECT id, name, version, space_id, value, created_at, updated_by
FROM configs WHERE name = ? AND version = ?;

-- name: ListUserConfigVersionsByName :many
SELECT id, name, version, space_id, value, created_at, updated_by
FROM configs WHERE name = ?
ORDER BY version ASC;

-- name: GetUserConfigByID :one
SELECT id, name, version, space_id, value, created_at, updated_by
FROM configs WHERE id = ?;

-- name: GetNextUserConfigVersion :one
SELECT COALESCE(MAX(version), 0) + 1
FROM configs
WHERE name = ?;

-- name: InsertUserConfig :one
INSERT INTO configs (name, version, space_id, value, created_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, name, version, space_id, value, created_at, updated_by;

-- name: RenameUserConfig :exec
UPDATE configs SET name = ? WHERE name = ?;

-- name: DeleteUserConfig :exec
DELETE FROM configs WHERE name = ?;

-- === assets ===

-- name: ListLatestAssets :many
SELECT a.id, a.key, a.space_id, a.asset_directory_id, a.created_by,
       v.id AS asset_version_id, v.created_at, v.version, v.location, v.size_bytes
FROM assets a
JOIN asset_versions v ON v.asset_id = a.id
JOIN (
    SELECT asset_id, MAX(version) AS version
    FROM asset_versions
    WHERE location NOT LIKE 'pending://%'
    GROUP BY asset_id
) latest ON latest.asset_id = v.asset_id AND latest.version = v.version
ORDER BY a.key;

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
WHERE space_id = ? AND parent_id = ? AND key = ?;

-- name: InsertAssetRow :one
INSERT INTO assets (space_id, key, asset_directory_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, key, asset_directory_id, created_at, created_by;

-- name: RenameAssetKey :exec
UPDATE assets SET key = ? WHERE id = ?;

-- name: DeleteAssetRow :exec
DELETE FROM assets WHERE id = ?;

-- name: ListPublishedAssetVersionIDs :many
SELECT asset_id, id, version FROM asset_versions
WHERE location NOT LIKE 'pending://%'
ORDER BY asset_id, version;

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

-- name: ListSecrets :many
SELECT id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by
FROM secrets ORDER BY name, version;

-- name: ListLatestSecrets :many
SELECT s.id, s.name, s.version, s.space_id, s.smk_version, s.ciphertext, s.nonce, s.created_at, s.updated_by
FROM secrets s
JOIN (
    SELECT name, MAX(version) AS version
    FROM secrets
    GROUP BY name
) latest ON latest.name = s.name AND latest.version = s.version
ORDER BY s.name;

-- name: GetSecret :one
SELECT id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by
FROM secrets WHERE name = ?
ORDER BY version DESC
LIMIT 1;

-- name: GetSecretVersion :one
SELECT id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by
FROM secrets WHERE name = ? AND version = ?;

-- name: GetSecretByID :one
SELECT id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by
FROM secrets WHERE id = ?;

-- name: GetNextSecretVersion :one
SELECT COALESCE(MAX(version), 0) + 1
FROM secrets
WHERE name = ?;

-- name: InsertSecret :one
INSERT INTO secrets (name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by;

-- name: RenameSecret :exec
UPDATE secrets SET name = ? WHERE name = ?;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE name = ?;

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

-- === local_runtime_inputs ===

-- name: ListLocalRuntimeInputs :many
SELECT kind, ref_id, ciphertext, nonce, fetched_at FROM local_runtime_inputs;

-- name: UpsertLocalRuntimeInput :exec
INSERT INTO local_runtime_inputs (kind, ref_id, ciphertext, nonce, fetched_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(kind, ref_id) DO UPDATE SET
    ciphertext = excluded.ciphertext,
    nonce      = excluded.nonce,
    fetched_at = excluded.fetched_at;

-- name: DeleteLocalRuntimeInput :exec
DELETE FROM local_runtime_inputs WHERE kind = ? AND ref_id = ?;
