-- users is a materialised view of the write log, one row per live account,
-- maintained by the reducer in pq/materialise.go.

CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    data_blob    BLOB    NOT NULL,   -- InternalUser: WebAuthn id and credentials
    created_time INTEGER NOT NULL,   -- epoch ms; 0 predates the fact
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,   -- epoch ms
    author       INTEGER NOT NULL
);

-- Agent and user sessions are materialised views of the write log too, one
-- row per session keyed by its stream entity id, with the id inside the
-- token as session_id. Sessions are never deleted.
CREATE TABLE IF NOT EXISTS agent_sessions (
    id                 INTEGER PRIMARY KEY,
    session_id         TEXT    NOT NULL UNIQUE,  -- the id inside the a_ token
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,   -- epoch s
    expires_at         INTEGER NOT NULL,   -- epoch s; 0 until the token is minted
    token_hash         BLOB    NOT NULL,   -- SHA-256; the plaintext is never stored
    token_prefix       TEXT    NOT NULL,
    status             INTEGER NOT NULL,   -- AgentSessionStatus
    requesting_address TEXT    NOT NULL,
    approval_code      TEXT    NOT NULL,
    approved_at        INTEGER NOT NULL,   -- epoch s
    seq                INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,   -- epoch ms
    author             INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_user ON agent_sessions (user_id, created_at);

CREATE TABLE IF NOT EXISTS user_sessions (
    id                 INTEGER PRIMARY KEY,
    session_id         TEXT    NOT NULL UNIQUE,  -- the id inside the u_ token
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,   -- epoch s
    expires_at         INTEGER NOT NULL,   -- epoch s
    token_hash         BLOB    NOT NULL,   -- SHA-256; the plaintext is never stored
    revoked_at         INTEGER NOT NULL,   -- epoch s; 0 while live
    kind               INTEGER NOT NULL,   -- UserSessionKind: 0 full, 1 bootstrap
    requesting_address TEXT    NOT NULL,
    user_agent         TEXT    NOT NULL,
    seq                INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,   -- epoch ms
    author             INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions (user_id, created_at);
