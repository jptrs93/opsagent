-- The complete secondary (worker) schema. A worker's database holds only
-- machine-local runtime state: the durable assignment blobs pushed by the
-- primary, the append-only status history the worker itself writes, sealed
-- copies of the runtime inputs its workloads need for cold start, and small
-- machine-local key/value state. Nothing here is replicated, and none of the
-- primary's control-plane data (users, secrets, configs, assets, nodes) ever
-- touches this file — a copy of secondary.db carries no cluster credentials
-- or cross-node state.
--
-- scheduled_instance_status is the one table whose shape is shared with the
-- primary: the worker appends rows locally and streams them to the primary,
-- which stores its cluster-wide copy under the same definition in primarydb.

-- Small machine-local key/value state, e.g. the worker's cached cluster network
-- parameters (programmed into the network on boot before the primary is
-- reachable). Never replicated.
CREATE TABLE IF NOT EXISTS local_kv (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
);

-- Secondary-local encrypted copies of the runtime inputs (secret and config
-- values) referenced by the instances assigned to this node, so a worker can
-- cold-start its workloads while the primary is unreachable. Never replicated;
-- populated only by prepare on the node that needs the value.
--
-- Each row is sealed under this node's own machine KEK, which is independent of
-- the primary's key hierarchy and lives outside the DB — so this file alone
-- decrypts nowhere, including on the primary. There is deliberately no key
-- hierarchy and no recovery slot: the primary is authoritative, so a lost
-- machine key just means refetching.
--
-- Rows never go stale. Secret and config rows are immutable and versioned, so
-- ref_id always denotes the same value; rotation mints a new id and reaches this
-- node as a new deployment config version. A row can only become unreferenced,
-- which is what the retention sweep collects.
CREATE TABLE IF NOT EXISTS local_runtime_inputs (
    kind       INTEGER NOT NULL,  -- 1=secret, 2=config
    ref_id     INTEGER NOT NULL,  -- immutable secrets.id / configs.id
    ciphertext BLOB    NOT NULL,  -- AEAD(value, machine KEK), AAD = kind + ref_id
    nonce      BLOB    NOT NULL,
    fetched_at INTEGER NOT NULL,  -- epoch ms
    PRIMARY KEY (kind, ref_id)
);

-- Durable copy of each non-final ScheduledInstanceState blob pushed by the
-- primary. This is the worker's source of truth for what it should be running:
-- each blob is a full assignment with its pinned config version, so workloads
-- cold-start without the primary being reachable.
CREATE TABLE IF NOT EXISTS local_scheduled_instance_cache (
    instance_id INTEGER PRIMARY KEY,
    blob        BLOB NOT NULL
);

-- Append-only observed status for each scheduled instance this node runs.
-- Latest row per scheduled_instance_id is the current status. Same shape as the
-- primary's table of the same name; the secondary's rows are forwarded to the
-- primary over the cluster stream but this local copy is never replicated.
--
-- Unlike the primary, there is no deployment_id index: the per-deployment
-- history query is a primary-side display concern, and every secondary read here
-- is covered by the primary key.
CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    deployment_id           INTEGER NOT NULL DEFAULT 0,
    preparer_spec_version   INTEGER,
    preparer_artifact       TEXT,
    -- The two preparation stages. There is no stored rollup: it is derived from
    -- this pair on read, so the two cannot disagree with it. Not nullable, since
    -- zero already means UNKNOWN and presence is decided by
    -- preparer_spec_version.
    preparer_inputs_status  INTEGER NOT NULL DEFAULT 0,  -- stage 1: assets/secrets/configs
    preparer_image_status   INTEGER NOT NULL DEFAULT 0,  -- stage 2: build, pull, or download
    runner_spec_version     INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    runner_exit_code        INTEGER,
    PRIMARY KEY (scheduled_instance_id, updated_at)
);
