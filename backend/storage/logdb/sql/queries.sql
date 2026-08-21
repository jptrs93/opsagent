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
SELECT id, deployment_id, day, level, node, seq, min_time, max_time, row_count, byte_size, created_at
FROM log_files WHERE deployment_id = ?
ORDER BY max_time DESC, min_time DESC, id DESC;
