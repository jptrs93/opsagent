-- === deployment_configs ===

-- CreateDeploymentConfig inserts a brand-new deployment, auto-allocating the
-- integer deployment_id. On (environment, machine, name) conflict it revives the
-- existing row (e.g. a previously soft-deleted one) keeping its original
-- deployment_id and created_at, and returns both.
-- name: CreateDeploymentConfig :one
INSERT INTO deployment_configs (environment, machine, name, created_at, version, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(environment, machine, name) DO UPDATE SET
    version = excluded.version,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    spec_blob = excluded.spec_blob,
    desired_version = excluded.desired_version,
    desired_running = excluded.desired_running,
    deleted = excluded.deleted
RETURNING deployment_id, created_at;

-- name: GetDeploymentConfig :one
SELECT deployment_id, environment, machine, name, created_at, version, updated_at, updated_by,
       spec_blob, desired_version, desired_running, deleted
FROM deployment_configs
WHERE deployment_id = ?;

-- name: UpsertDeploymentConfig :exec
INSERT INTO deployment_configs (deployment_id, environment, machine, name, created_at, version, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    environment = excluded.environment,
    machine = excluded.machine,
    name = excluded.name,
    created_at = excluded.created_at,
    version = excluded.version,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    spec_blob = excluded.spec_blob,
    desired_version = excluded.desired_version,
    desired_running = excluded.desired_running,
    deleted = excluded.deleted;

-- name: UpdateDesiredState :exec
UPDATE deployment_configs
SET desired_version = ?, desired_running = ?, version = version + 1, updated_at = ?, updated_by = ?
WHERE deployment_id = ?;

-- name: ListDeploymentConfigsByMachine :many
SELECT deployment_id, environment, machine, name, created_at, version, updated_at, updated_by,
       spec_blob, desired_version, desired_running, deleted
FROM deployment_configs
WHERE machine = ? AND deleted = 0;

-- name: ListAllDeploymentConfigs :many
SELECT deployment_id, environment, machine, name, created_at, version, updated_at, updated_by,
       spec_blob, desired_version, desired_running, deleted
FROM deployment_configs
WHERE deleted = 0;

-- === deployment_config_history ===

-- name: InsertDeploymentConfigHistory :exec
INSERT INTO deployment_config_history (deployment_id, version, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentConfigHistory :many
SELECT deployment_id, version, updated_at, updated_by, spec_blob,
       desired_version, desired_running, deleted
FROM deployment_config_history
WHERE deployment_id = ?
ORDER BY version ASC;

-- name: GetConfigHistoryDesiredVersion :one
SELECT desired_version
FROM deployment_config_history
WHERE deployment_id = ? AND version = ?;

-- === deployment_status ===

-- name: UpsertDeploymentStatus :exec
INSERT INTO deployment_status (
    deployment_id, updated_at,
    preparer_config_version, preparer_artifact, preparer_status,
    runner_config_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    updated_at = excluded.updated_at,
    preparer_config_version = excluded.preparer_config_version,
    preparer_artifact = excluded.preparer_artifact,
    preparer_status = excluded.preparer_status,
    runner_config_version = excluded.runner_config_version,
    runner_pid = excluded.runner_pid,
    runner_artifact = excluded.runner_artifact,
    runner_status = excluded.runner_status,
    runner_num_restarts = excluded.runner_num_restarts,
    runner_last_restart_at = excluded.runner_last_restart_at;

-- name: ListAllDeploymentStatuses :many
SELECT deployment_id, updated_at,
       preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status;

-- === deployment_status_history ===

-- name: InsertDeploymentStatusHistory :exec
INSERT INTO deployment_status_history (
    deployment_id, updated_at,
    preparer_config_version, preparer_artifact, preparer_status,
    runner_config_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentStatusHistory :many
SELECT deployment_id, updated_at,
       preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status_history
WHERE deployment_id = ?
ORDER BY updated_at ASC;

-- name: ListDeploymentStatusHistorySince :many
SELECT deployment_id, updated_at,
       preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status_history
WHERE deployment_id = ? AND updated_at > ?
ORDER BY updated_at ASC;

-- === users ===

-- name: GetUser :one
SELECT id, name, data_blob FROM users WHERE id = ?;

-- name: UpsertUser :exec
INSERT INTO users (id, name, data_blob) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob;

-- name: ListUsers :many
SELECT id, name, data_blob FROM users ORDER BY id;

-- === public_keys ===

-- name: GetPublicKey :one
SELECT kid, key_bytes FROM public_keys WHERE kid = ?;

-- name: UpsertPublicKey :exec
INSERT INTO public_keys (kid, key_bytes) VALUES (?, ?)
ON CONFLICT(kid) DO UPDATE SET key_bytes = excluded.key_bytes;

-- name: ListPublicKeys :many
SELECT kid, key_bytes FROM public_keys ORDER BY kid;

-- === system_config ===

-- name: GetConfigValue :one
SELECT key, value, updated_at FROM system_config WHERE key = ?;

-- name: UpsertConfigValue :exec
INSERT INTO system_config (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = excluded.updated_at;

-- === user_configs ===

-- name: ListUserConfigs :many
SELECT name, config_group, value, created_at, updated_at, updated_by
FROM user_configs ORDER BY name;

-- name: GetUserConfig :one
SELECT name, config_group, value, created_at, updated_at, updated_by
FROM user_configs WHERE name = ?;

-- name: UpsertUserConfig :exec
INSERT INTO user_configs (name, config_group, value, created_at, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    config_group = excluded.config_group,
    value = excluded.value,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by;

-- name: DeleteUserConfig :exec
DELETE FROM user_configs WHERE name = ?;

-- === assets ===

-- name: ListLatestAssets :many
SELECT a.id, a.key, a.created_at, a.version, a.format, a.location, length(a.blob) AS size_bytes
FROM assets a
JOIN (
    SELECT key, MAX(version) AS version
    FROM assets
    GROUP BY key
) latest ON latest.key = a.key AND latest.version = a.version
ORDER BY a.key;

-- name: GetLatestAsset :one
SELECT id, key, created_at, version, format, location, blob
FROM assets
WHERE key = ?
ORDER BY version DESC
LIMIT 1;

-- name: GetAssetVersion :one
SELECT id, key, created_at, version, format, location, blob
FROM assets
WHERE key = ? AND version = ?;

-- name: GetAssetByIDVersion :one
SELECT id, key, created_at, version, format, location, blob
FROM assets
WHERE id = ? AND version = ?;

-- name: GetNextAssetVersion :one
SELECT COALESCE(MAX(version), 0) + 1
FROM assets
WHERE key = ?;

-- name: InsertAsset :one
INSERT INTO assets (key, created_at, version, format, location, blob)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, key, created_at, version, format, location, blob;

-- name: DeleteAsset :exec
DELETE FROM assets WHERE key = ?;

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
SELECT name, secret_group, smk_version, ciphertext, nonce, created_at, updated_at, updated_by
FROM secrets ORDER BY name;

-- name: UpsertSecret :exec
INSERT INTO secrets (name, secret_group, smk_version, ciphertext, nonce, created_at, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    secret_group = excluded.secret_group,
    smk_version = excluded.smk_version,
    ciphertext = excluded.ciphertext,
    nonce = excluded.nonce,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by;

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
