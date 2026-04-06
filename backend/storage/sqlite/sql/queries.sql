-- === deployment_identifiers ===

-- name: GetDeploymentIdentifier :one
SELECT id, environment, machine, name, created_at
FROM deployment_identifiers
WHERE environment = ? AND machine = ? AND name = ?;

-- name: GetDeploymentIdentifierByID :one
SELECT id, environment, machine, name, created_at
FROM deployment_identifiers
WHERE id = ?;

-- name: InsertDeploymentIdentifier :one
INSERT INTO deployment_identifiers (environment, machine, name, created_at)
VALUES (?, ?, ?, ?)
RETURNING id, environment, machine, name, created_at;

-- === deployment_configs ===

-- name: GetDeploymentConfig :one
SELECT deployment_id, seq_no, updated_at, updated_by, spec_blob,
       desired_version, desired_running, deleted
FROM deployment_configs
WHERE deployment_id = ?;

-- name: UpsertDeploymentConfig :exec
INSERT INTO deployment_configs (deployment_id, seq_no, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    seq_no = excluded.seq_no,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by,
    spec_blob = excluded.spec_blob,
    desired_version = excluded.desired_version,
    desired_running = excluded.desired_running,
    deleted = excluded.deleted;

-- name: UpdateDesiredState :exec
UPDATE deployment_configs
SET desired_version = ?, desired_running = ?, seq_no = seq_no + 1, updated_at = ?, updated_by = ?
WHERE deployment_id = ?;

-- name: ListDeploymentConfigsByMachine :many
SELECT dc.deployment_id, dc.seq_no, dc.updated_at, dc.updated_by, dc.spec_blob,
       dc.desired_version, dc.desired_running, dc.deleted,
       di.environment, di.machine, di.name
FROM deployment_configs dc
JOIN deployment_identifiers di ON di.id = dc.deployment_id
WHERE di.machine = ? AND dc.deleted = 0;

-- name: ListAllDeploymentConfigs :many
SELECT dc.deployment_id, dc.seq_no, dc.updated_at, dc.updated_by, dc.spec_blob,
       dc.desired_version, dc.desired_running, dc.deleted,
       di.environment, di.machine, di.name
FROM deployment_configs dc
JOIN deployment_identifiers di ON di.id = dc.deployment_id
WHERE dc.deleted = 0;

-- === deployment_config_history ===

-- name: InsertDeploymentConfigHistory :exec
INSERT INTO deployment_config_history (deployment_id, seq_no, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentConfigHistory :many
SELECT deployment_id, seq_no, updated_at, updated_by, spec_blob,
       desired_version, desired_running, deleted
FROM deployment_config_history
WHERE deployment_id = ?
ORDER BY seq_no ASC;

-- === deployment_status_history ===

-- name: GetLatestDeploymentStatus :one
SELECT deployment_id, status_seq_no, timestamp,
       preparer_seq_no, preparer_artifact, preparer_status,
       runner_seq_no, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status_history
WHERE deployment_id = ?
ORDER BY status_seq_no DESC
LIMIT 1;

-- name: InsertDeploymentStatus :exec
INSERT INTO deployment_status_history (
    deployment_id, status_seq_no, timestamp,
    preparer_seq_no, preparer_artifact, preparer_status,
    runner_seq_no, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDeploymentStatusHistory :many
SELECT deployment_id, status_seq_no, timestamp,
       preparer_seq_no, preparer_artifact, preparer_status,
       runner_seq_no, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at
FROM deployment_status_history
WHERE deployment_id = ?
ORDER BY status_seq_no ASC;

-- name: ListLatestDeploymentStatuses :many
SELECT dsh.deployment_id, dsh.status_seq_no, dsh.timestamp,
       dsh.preparer_seq_no, dsh.preparer_artifact, dsh.preparer_status,
       dsh.runner_seq_no, dsh.runner_pid, dsh.runner_artifact, dsh.runner_status,
       dsh.runner_num_restarts, dsh.runner_last_restart_at
FROM deployment_status_history dsh
INNER JOIN (
    SELECT deployment_id, MAX(status_seq_no) AS max_seq
    FROM deployment_status_history
    GROUP BY deployment_id
) latest ON dsh.deployment_id = latest.deployment_id AND dsh.status_seq_no = latest.max_seq;

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
