CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535),
    name TEXT    NOT NULL DEFAULT ''
);

INSERT INTO spaces (id, name) VALUES (0, '_system'), (1, 'global') ON CONFLICT(id) DO UPDATE SET name = excluded.name;

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL,
    data_blob     BLOB    NOT NULL,
    created_at    INTEGER NOT NULL DEFAULT 0,  -- epoch ms; 0 predates the column
    last_login_at INTEGER NOT NULL DEFAULT 0   -- epoch ms; 0 = never logged in
);

CREATE TABLE IF NOT EXISTS public_keys (
    kid       TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id           TEXT    PRIMARY KEY,          -- the token's jti claim
    user_id      INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,             -- epoch ms
    expires_at   INTEGER NOT NULL,             -- epoch ms; 0 until the token is minted
    token_hash   BLOB    NOT NULL,             -- SHA-256; the plaintext is never stored
    token_prefix TEXT    NOT NULL,
    revoked_at   INTEGER NOT NULL DEFAULT 0,   -- epoch ms
    scopes       TEXT    NOT NULL DEFAULT '',
    status             INTEGER NOT NULL DEFAULT 2,  -- AgentSessionStatus
    requesting_address TEXT    NOT NULL DEFAULT '',
    approval_code      TEXT    NOT NULL DEFAULT '',
    approved_at        INTEGER NOT NULL DEFAULT 0   -- epoch ms
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_created
    ON agent_sessions (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS system_config_revisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,  -- epoch ms
    config_blob BLOB    NOT NULL
);

-- Machine-local state. Created on every node; never replicated.
CREATE TABLE IF NOT EXISTS local_kv (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
);

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

CREATE TABLE IF NOT EXISTS nodes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER,                       -- NULL on the primary
    enrolled_at   INTEGER NOT NULL DEFAULT 0,    -- epoch ms
    name          TEXT    NOT NULL,
    identifier    TEXT    NOT NULL DEFAULT '',
    roles         TEXT    NOT NULL DEFAULT '[]', -- JSON array of integer role ids
    addresses     TEXT    NOT NULL DEFAULT '[]', -- JSON array of node addresses
    wg_public_key TEXT    NOT NULL DEFAULT '',
    allowed_spaces TEXT   NOT NULL DEFAULT '[0]', -- JSON array of placeable space ids
    UNIQUE(name),
    UNIQUE(identifier),
    UNIQUE(enrollment_id)
);

CREATE TABLE IF NOT EXISTS node_statuses (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id           INTEGER NOT NULL,
    last_connected_at INTEGER NOT NULL DEFAULT 0,  -- epoch ms
    is_connected      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(node_id)
);
