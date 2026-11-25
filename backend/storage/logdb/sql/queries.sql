-- name: GetLogStreamCommitMarker :one
SELECT deployment_id, day, bucket, record_time, byte_offset, updated_at, file
FROM log_stream_commit_marker WHERE deployment_id = ?;

-- name: UpsertLogStreamCommitMarker :exec
INSERT INTO log_stream_commit_marker (deployment_id, day, bucket, record_time, byte_offset, updated_at, file)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO UPDATE SET
    day = excluded.day,
    bucket = excluded.bucket,
    record_time = excluded.record_time,
    byte_offset = excluded.byte_offset,
    updated_at = excluded.updated_at,
    file = excluded.file;

-- name: InsertLogFile :one
INSERT INTO log_files (deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: ListLogFilesNewestFirst :many
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at, sha256
FROM log_files WHERE deployment_id = ?
ORDER BY max_time DESC, min_time DESC, id DESC;

-- name: GetLogFileBySeq :one
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at, sha256
FROM log_files WHERE deployment_id = ? AND day = ? AND seq = ?;

-- name: ListLogFilesForDay :many
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at, sha256
FROM log_files WHERE deployment_id = ? AND day = ?
ORDER BY min_time, id;

-- name: ListUnsealedLogFilesForDayLevel :many
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at, sha256
FROM log_files WHERE deployment_id = ? AND day = ? AND level = ? AND sha256 = ''
ORDER BY min_time, id;

-- name: ListSealableLogFiles :many
SELECT f.id, f.deployment_id, f.day, f.level, f.node, f.seq, f.min_time, f.max_time, f.row_count, f.byte_size, f.created_at, f.sha256
FROM log_files AS f
WHERE f.sha256 = '' AND f.deployment_id != 0
  AND (f.level = 2 OR (f.day < sqlc.arg('before_day') AND NOT EXISTS (
        SELECT 1 FROM log_files AS o
        WHERE o.deployment_id = f.deployment_id AND o.day = f.day AND o.level = 1 AND o.sha256 = '' AND o.id != f.id)))
ORDER BY f.day, f.id
LIMIT sqlc.arg('max_files');

-- name: SetLogFileSha256 :execrows
UPDATE log_files SET sha256 = ? WHERE id = ? AND sha256 = '';

-- name: ListSealedLogFiles :many
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at, sha256
FROM log_files WHERE sha256 != '' AND node = ? AND deployment_id != 0
ORDER BY day, id;

-- name: ListLogFileDeployments :many
SELECT DISTINCT deployment_id FROM log_files ORDER BY deployment_id;

-- name: ListLogFileDaysBefore :many
SELECT DISTINCT deployment_id, day FROM log_files WHERE day < ? ORDER BY deployment_id, day;

-- name: ListLevelOneDays :many
SELECT deployment_id, day, COUNT(*) AS file_count, SUM(byte_size) AS byte_total
FROM log_files WHERE level = 1 AND sha256 = '' GROUP BY deployment_id, day ORDER BY deployment_id, day;

-- name: DeleteLogFile :exec
DELETE FROM log_files WHERE id = ?;

-- name: DeleteLogFilesForDay :exec
DELETE FROM log_files WHERE deployment_id = ? AND day = ?;

-- name: InsertLogFileKey :exec
INSERT INTO log_file_keys (file_id, key, type, placement, row_count)
VALUES (?, ?, ?, ?, ?);

-- name: DeleteLogFileKeys :exec
DELETE FROM log_file_keys WHERE file_id = ?;

-- name: DeleteLogFileKeysForDay :exec
DELETE FROM log_file_keys WHERE file_id IN (SELECT id FROM log_files WHERE deployment_id = ? AND day = ?);

-- name: ListLogFileKeysInRange :many
SELECT k.file_id, k.key, k.type, k.placement, k.row_count
FROM log_file_keys k JOIN log_files f ON f.id = k.file_id
WHERE f.deployment_id = ? AND f.max_time >= ? AND f.min_time < ? AND k.key IN (sqlc.slice('keys'));
