CREATE TABLE IF NOT EXISTS log_chunks (
    id            INTEGER PRIMARY KEY,
    node_id       INTEGER NOT NULL,
    deployment_id INTEGER NOT NULL,
    day           INTEGER NOT NULL,  -- UTC days since the epoch
    level         INTEGER NOT NULL,
    file_seq      INTEGER NOT NULL,  -- the file's own number on the node
    min_time      INTEGER NOT NULL,  -- epoch ns
    max_time      INTEGER NOT NULL,  -- epoch ns
    row_count     INTEGER NOT NULL,
    size_bytes    INTEGER NOT NULL,
    sha256        BLOB    NOT NULL,
    is_local      INTEGER NOT NULL,
    archive       INTEGER NOT NULL,  -- 0 = none, 1 = s3
    archived_time INTEGER NOT NULL,  -- epoch ms of the write that first set archive; 0 = never
    created_time  INTEGER NOT NULL,  -- epoch ms
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_log_chunks_identity ON log_chunks (node_id, deployment_id, day, file_seq);

CREATE INDEX IF NOT EXISTS idx_log_chunks_node_day ON log_chunks (node_id, day);

CREATE INDEX IF NOT EXISTS idx_log_chunks_to_archive ON log_chunks (node_id, day, id) WHERE is_local = 1 AND archive = 0;

CREATE TABLE IF NOT EXISTS deployment_metrics_chunks (
    id            INTEGER PRIMARY KEY,
    node_id       INTEGER NOT NULL,
    deployment_id INTEGER NOT NULL,
    day           INTEGER NOT NULL,  -- UTC days since the epoch
    file_seq      INTEGER NOT NULL,  -- the file's own number on the node
    min_time      INTEGER NOT NULL,  -- epoch ms
    max_time      INTEGER NOT NULL,  -- epoch ms
    row_count     INTEGER NOT NULL,
    size_bytes    INTEGER NOT NULL,
    sha256        BLOB    NOT NULL,
    is_local      INTEGER NOT NULL,
    archive       INTEGER NOT NULL,  -- 0 = none, 1 = s3
    archived_time INTEGER NOT NULL,  -- epoch ms of the write that first set archive; 0 = never
    created_time  INTEGER NOT NULL,  -- epoch ms
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_metrics_chunks_identity ON deployment_metrics_chunks (node_id, deployment_id, day, file_seq);

CREATE INDEX IF NOT EXISTS idx_deployment_metrics_chunks_node_day ON deployment_metrics_chunks (node_id, day);

CREATE INDEX IF NOT EXISTS idx_deployment_metrics_chunks_to_archive ON deployment_metrics_chunks (node_id, day, id) WHERE is_local = 1 AND archive = 0;

CREATE TABLE IF NOT EXISTS node_metrics_chunks (
    id            INTEGER PRIMARY KEY,
    node_id       INTEGER NOT NULL,
    day           INTEGER NOT NULL,  -- UTC days since the epoch
    file_seq      INTEGER NOT NULL,  -- the file's own number on the node
    min_time      INTEGER NOT NULL,  -- epoch ms
    max_time      INTEGER NOT NULL,  -- epoch ms
    row_count     INTEGER NOT NULL,
    size_bytes    INTEGER NOT NULL,
    sha256        BLOB    NOT NULL,
    is_local      INTEGER NOT NULL,
    archive       INTEGER NOT NULL,  -- 0 = none, 1 = s3
    archived_time INTEGER NOT NULL,  -- epoch ms of the write that first set archive; 0 = never
    created_time  INTEGER NOT NULL,  -- epoch ms
    seq           INTEGER NOT NULL,
    event_time    INTEGER NOT NULL,  -- epoch ms
    author        INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_node_metrics_chunks_identity ON node_metrics_chunks (node_id, day, file_seq);

CREATE INDEX IF NOT EXISTS idx_node_metrics_chunks_to_archive ON node_metrics_chunks (node_id, day, id) WHERE is_local = 1 AND archive = 0;
