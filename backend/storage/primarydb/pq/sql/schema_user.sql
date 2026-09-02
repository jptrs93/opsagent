CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL,
    data_blob     BLOB    NOT NULL,
    created_at    INTEGER NOT NULL DEFAULT 0,  -- epoch ms; 0 predates the column
    last_login_at INTEGER NOT NULL DEFAULT 0   -- epoch ms; 0 = never logged in
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

CREATE TABLE IF NOT EXISTS personal_sessions (
    id                 TEXT    PRIMARY KEY,
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    token_hash         BLOB    NOT NULL,
    revoked_at         INTEGER NOT NULL DEFAULT 0,
    requesting_address TEXT    NOT NULL DEFAULT '',
    user_agent         TEXT    NOT NULL DEFAULT '',
    last_active_at     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_personal_sessions_user_created
    ON personal_sessions (user_id, created_at DESC);
