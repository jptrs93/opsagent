-- === deployment_identifiers ===
-- Only used at config-save time to map (env, machine, name) to integer id.

-- name: UpsertDeploymentID :one
INSERT INTO deployment_identifiers (environment, machine, name, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(environment, machine, name) DO UPDATE SET
    created_at = deployment_identifiers.created_at
RETURNING id;

-- === deployment_configs ===

-- name: GetDeploymentConfig :one
SELECT deployment_id, environment, machine, name, version, updated_at, updated_by,
       spec_blob, desired_version, desired_running, deleted
FROM deployment_configs
WHERE deployment_id = ?;

-- name: UpsertDeploymentConfig :exec
INSERT INTO deployment_configs (deployment_id, environment, machine, name, version, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    environment = excluded.environment,
    machine = excluded.machine,
    name = excluded.name,
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
SELECT deployment_id, environment, machine, name, version, updated_at, updated_by,
       spec_blob, desired_version, desired_running, deleted
FROM deployment_configs
WHERE machine = ? AND deleted = 0;

-- name: ListAllDeploymentConfigs :many
SELECT deployment_id, environment, machine, name, version, updated_at, updated_by,
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

-- === deployment_status ===

-- name: UpsertDeploymentStatus :exec
INSERT INTO deployment_status (
    deployment_id, status_seq_no, timestamp,
    preparer_config_version, preparer_artifact, preparer_status,
    runner_config_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    status_seq_no = excluded.status_seq_no,
    timestamp = excluded.timestamp,
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
SELECT deployment_id, status_seq_no, timestamp,
       preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status;

-- === deployment_status_history ===

-- name: InsertDeploymentStatusHistory :exec
INSERT INTO deployment_status_history (
    deployment_id, status_seq_no, timestamp,
    preparer_config_version, preparer_artifact, preparer_status,
    runner_config_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentStatusHistory :many
SELECT deployment_id, status_seq_no, timestamp,
       preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status_history
WHERE deployment_id = ?
ORDER BY status_seq_no ASC;

-- === user_config_versions ===

-- name: GetLatestUserConfigVersion :one
SELECT version, timestamp, updated_by, yaml_content
FROM user_config_versions
ORDER BY version DESC
LIMIT 1;

-- name: InsertUserConfigVersion :one
INSERT INTO user_config_versions (version, timestamp, updated_by, yaml_content)
VALUES (
    (SELECT COALESCE(MAX(version), 0) + 1 FROM user_config_versions),
    ?, ?, ?
)
RETURNING version, timestamp, updated_by, yaml_content;

-- name: ListUserConfigVersions :many
SELECT version, timestamp, updated_by, yaml_content
FROM user_config_versions
ORDER BY version DESC;

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
