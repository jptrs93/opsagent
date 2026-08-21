CREATE TABLE IF NOT EXISTS log_files (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  deployment_id INTEGER NOT NULL,
  day           INTEGER NOT NULL,
  level         INTEGER NOT NULL,
  node          INTEGER NOT NULL,
  seq           INTEGER NOT NULL,
  min_time      INTEGER NOT NULL,
  max_time      INTEGER NOT NULL,
  row_count     INTEGER NOT NULL,
  byte_size     INTEGER NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS log_files_path ON log_files(deployment_id, day, seq);
CREATE INDEX IF NOT EXISTS log_files_scan ON log_files(deployment_id, max_time DESC, min_time);

CREATE TABLE IF NOT EXISTS log_stream_commit_marker (
    deployment_id  INTEGER NOT NULL,
    day            INTEGER NOT NULL,
    bucket         INTEGER NOT NULL,
    record_time    INTEGER NOT NULL,
    byte_offset    INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    file           TEXT NOT NULL,
    PRIMARY KEY (deployment_id)
) WITHOUT ROWID;
