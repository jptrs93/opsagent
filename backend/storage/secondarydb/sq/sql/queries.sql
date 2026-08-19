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

-- name: UpsertLocalScheduledInstanceCache :exec
INSERT INTO local_scheduled_instance_cache (instance_id, blob)
VALUES (?, ?)
ON CONFLICT(instance_id) DO UPDATE SET blob = excluded.blob;

-- name: DeleteLocalScheduledInstanceCache :exec
DELETE FROM local_scheduled_instance_cache WHERE instance_id = ?;

-- name: ListLocalScheduledInstanceCache :many
SELECT instance_id, blob FROM local_scheduled_instance_cache;

-- name: GetLocalKV :one
SELECT value FROM local_kv WHERE key = ?;

-- name: UpsertLocalKV :exec
INSERT INTO local_kv (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

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
