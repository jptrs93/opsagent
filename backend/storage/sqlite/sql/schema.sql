-- Config representing desired state for each deployment. One row per deployment,
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
    version         INTEGER NOT NULL DEFAULT 0,
    node_id         INTEGER NOT NULL DEFAULT -1,
    space_id        INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
    name            TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0,  -- epoch ms; creation time of this deployment
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_active_node_identity
    ON deployment_configs(node_id, space_id, name)
    WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535),
    name TEXT    NOT NULL DEFAULT ''
);

INSERT INTO spaces (id, name) VALUES (0, 'opendeploy'), (1, 'default') ON CONFLICT(id) DO UPDATE SET name = excluded.name;

-- Append-only log of every config mutation.
CREATE TABLE IF NOT EXISTS deployment_config_history (
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    space_id        INTEGER NOT NULL DEFAULT 1,
    node_id         INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (deployment_id, version)
);

-- One row per placement/runtime incarnation. state: 0=run, 1=terminate, 2=finalized.
CREATE TABLE IF NOT EXISTS scheduled_instances (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at         INTEGER NOT NULL,  -- epoch ms
    deployment_id      INTEGER NOT NULL,
    deployment_version INTEGER NOT NULL,
    node_id            INTEGER NOT NULL,
    instance_ordinal   INTEGER NOT NULL DEFAULT 0,
    state              INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_deployment_ordinal
    ON scheduled_instances(deployment_id, instance_ordinal, id);

CREATE INDEX IF NOT EXISTS idx_scheduled_instances_node_active
    ON scheduled_instances(node_id, state);

CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_instances_unique_run
    ON scheduled_instances(deployment_id, deployment_version, node_id, instance_ordinal)
    WHERE state = 0;

-- Append-only observed status for each scheduled instance. Latest row per
-- scheduled_instance_id is the current status.
CREATE TABLE IF NOT EXISTS scheduled_instance_status (
    scheduled_instance_id   INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    deployment_id           INTEGER NOT NULL DEFAULT 0,
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    -- The two preparation stages. There is no stored rollup: it is derived from
    -- this pair on read, so the two cannot disagree with it. Not nullable, since
    -- zero already means UNKNOWN and presence is decided by
    -- preparer_config_version.
    preparer_inputs_status  INTEGER NOT NULL DEFAULT 0,  -- stage 1: assets/secrets/configs
    preparer_image_status   INTEGER NOT NULL DEFAULT 0,  -- stage 2: build, pull, or download
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    PRIMARY KEY (scheduled_instance_id, updated_at)
);

-- The primary key covers per-instance lookups. Deployment history spans every
-- instance of a deployment, and this table is append-only and unbounded, so
-- that query needs its own index to avoid a full scan that grows over time.
CREATE INDEX IF NOT EXISTS idx_scheduled_instance_status_deployment
    ON scheduled_instance_status(deployment_id, updated_at);

-- Secondary-local durable copy of each non-final ScheduledInstanceState blob.
CREATE TABLE IF NOT EXISTS local_scheduled_instance_cache (
    instance_id INTEGER PRIMARY KEY,
    blob        BLOB NOT NULL
);

-- Auth: passkey users.
CREATE TABLE IF NOT EXISTS users (
    id        INTEGER PRIMARY KEY,
    name      TEXT    NOT NULL,
    data_blob BLOB   NOT NULL
);

-- Auth: JWT signing keys.
CREATE TABLE IF NOT EXISTS public_keys (
    kid       TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL
);

-- Auth: agent sessions. Command-line and agent bearer tokens, tracked so they
-- can be listed and revoked. id doubles as the token's jti claim, which is how
-- VerifyAuth finds the row. Only the SHA-256 of the token is stored: the
-- plaintext is shown once at creation and is not recoverable, so a copy of this
-- database (including an off-box Litestream backup) carries no usable
-- credential. token_prefix is the leading characters, kept for display only.
CREATE TABLE IF NOT EXISTS agent_sessions (
    id           TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    token_hash   BLOB    NOT NULL,
    token_prefix TEXT    NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,
    scopes       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_created
    ON agent_sessions (user_id, created_at DESC);

-- Append-only revisions of OpenDeploy's own system configuration and settings.
CREATE TABLE IF NOT EXISTS system_config_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,
    config_blob BLOB    NOT NULL
);

-- Plain user-managed configuration values. These are intentionally not
-- encrypted at rest; encrypted credentials belong in secrets instead.
CREATE TABLE IF NOT EXISTS configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    value        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);

-- Versioned user-managed file assets. Rows are immutable: editing an asset
-- appends a new version for the same key. Small assets are stored inline in
-- blob; large assets store their object reference in location.
CREATE TABLE IF NOT EXISTS assets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT    NOT NULL,
    space_id    INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,  -- epoch ms; creation time of this version
    version     INTEGER NOT NULL,
    format      TEXT    NOT NULL DEFAULT '',
    location    TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    blob        BLOB    NOT NULL,
    UNIQUE (key, version)
);

CREATE INDEX IF NOT EXISTS idx_assets_key_created_at
    ON assets(key, created_at);

-- One durable job records each complete large-asset storage transition. Asset
-- locations remain authoritative for per-asset progress.
CREATE TABLE IF NOT EXISTS asset_migrations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    old_config_version_id INTEGER NOT NULL,
    new_config_version_id INTEGER NOT NULL,
    status                TEXT    NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'running', 'finished')),
    last_error            TEXT    NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    started_at            INTEGER NOT NULL DEFAULT 0,
    last_attempt_at       INTEGER NOT NULL DEFAULT 0,
    finished_at           INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_migrations_unfinished
    ON asset_migrations ((1))
    WHERE status != 'finished';

-- Secrets: envelope-encrypted key/value store. PRIMARY-ONLY — these two tables
-- are never replicated to secondaries (the cluster feeder only sends deployment
-- configs/status; see primary/session.go). They are created on every node by
-- the shared schema but stay empty off the primary.

-- Wrapped copies of the Secrets Master Key (SMK), one row per unlock method
-- ("slot"). The SMK encrypts every secret value; each slot stores the SMK
-- sealed under a different key-encryption key (the machine key, the recovery
-- code, ...). Adding/rotating a slot never re-encrypts the secrets themselves.
CREATE TABLE IF NOT EXISTS secret_keyslots (
    slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
    smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
    created_at  INTEGER NOT NULL          -- epoch ms
);

-- Encrypted user secret values, keyed by plaintext name and immutable version
-- (e.g. 'staging.db.password' v1). The value is AEAD-sealed under the SMK with
-- the name bound as associated data, so rename must re-encrypt every version.
CREATE TABLE IF NOT EXISTS secrets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    smk_version  INTEGER NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);

-- Encrypted OpenDeploy-managed system secrets. These are not visible through
-- user-facing secret CRUD and use a separate AEAD associated-data class.
CREATE TABLE IF NOT EXISTS system_secrets (
    name        TEXT PRIMARY KEY,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    updated_at  INTEGER NOT NULL   -- epoch ms
);

-- Small machine-local key/value state, e.g. the worker's cached cluster network
-- parameters (programmed into the network on boot before the primary is
-- reachable). Created on every node; never replicated.
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

-- Worker enrollment requests. A reconnecting unenrolled worker is identified by
-- requesting_machine_id and updates its existing row back to waiting.
CREATE TABLE IF NOT EXISTS enrollment_requests (
    id                       INTEGER PRIMARY KEY,
    created_at               INTEGER NOT NULL,  -- epoch ms
    updated_at               INTEGER NOT NULL,  -- epoch ms
    requesting_ip_address    TEXT    NOT NULL DEFAULT '',
    requesting_machine_id    TEXT    NOT NULL,
    opendeploy_version       TEXT    NOT NULL DEFAULT '',
    underlay_address         TEXT    NOT NULL DEFAULT '',
    status                   TEXT    NOT NULL DEFAULT 'waiting'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_requests_machine_id ON enrollment_requests(requesting_machine_id);

-- Canonical cluster node registry. The primary is inserted with enrollment_id
-- NULL; enrolled workers reference the enrollment request that accepted them.
CREATE TABLE IF NOT EXISTS nodes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER,
    enrolled_at   INTEGER NOT NULL DEFAULT 0,  -- epoch ms
    name          TEXT    NOT NULL,
    identifier    TEXT    NOT NULL DEFAULT '',
    roles         TEXT    NOT NULL DEFAULT '[]', -- JSON array of integer role ids
    addresses     TEXT    NOT NULL DEFAULT '[]', -- JSON array of node addresses
    wg_public_key TEXT    NOT NULL DEFAULT '',
    UNIQUE(name),
    UNIQUE(identifier),
    UNIQUE(enrollment_id)
);

CREATE TABLE IF NOT EXISTS node_statuses (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id           INTEGER NOT NULL,
    last_connected_at INTEGER NOT NULL DEFAULT 0, -- epoch ms
    is_connected      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(node_id)
);
