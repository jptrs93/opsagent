-- Core schema. Table groups with their own file: deployments
-- (schema_deployments.sql), secrets (schema_secrets.sql), configs
-- (schema_configs.sql), assets (schema_assets.sql).

CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535),
    name TEXT    NOT NULL DEFAULT ''
);

INSERT INTO spaces (id, name) VALUES (0, 'opendeploy'), (1, 'default') ON CONFLICT(id) DO UPDATE SET name = excluded.name;

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
    -- Zero until the token is minted. A session approved but not yet collected
    -- has no expiry because its 6h clock has not started.
    expires_at   INTEGER NOT NULL,
    token_hash   BLOB    NOT NULL,
    token_prefix TEXT    NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,
    scopes       TEXT    NOT NULL DEFAULT '',
    -- AgentSessionStatus. Authoritative state; revoked_at survives only as the
    -- timestamp that goes with status = REVOKED.
    status             INTEGER NOT NULL DEFAULT 2,
    requesting_address TEXT    NOT NULL DEFAULT '',
    approval_code      TEXT    NOT NULL DEFAULT '',
    approved_at        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_created
    ON agent_sessions (user_id, created_at DESC);

-- Append-only revisions of OpenDeploy's own system configuration and settings.
CREATE TABLE IF NOT EXISTS system_config_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,
    config_blob BLOB    NOT NULL
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
    -- JSON array of space ids whose deployments may be placed on this node.
    -- Inserts populate it with every space that exists; this default is only a
    -- floor, and the opendeploy space is unioned back in on every read anyway.
    allowed_spaces TEXT   NOT NULL DEFAULT '[0]',
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
