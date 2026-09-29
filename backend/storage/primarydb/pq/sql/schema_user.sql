-- Every table here is append-only: one row per event, live state is the
-- newest row per entity id. Rows are never updated or deleted.

CREATE TABLE IF NOT EXISTS user_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,   -- epoch ms
    author      INTEGER NOT NULL,
    user_id     INTEGER NOT NULL,
    event_type  INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update / 3 delete
    name        TEXT    NOT NULL,
    data_blob   BLOB    NOT NULL,   -- InternalUser: WebAuthn id and credentials
    created_at  INTEGER NOT NULL DEFAULT 0   -- epoch ms, copied forward; 0 predates the column
);

CREATE INDEX IF NOT EXISTS idx_user_event_log_user_id ON user_event_log (user_id, id);

CREATE TABLE IF NOT EXISTS agent_session_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,   -- epoch ms
    author             INTEGER NOT NULL,
    session_id         TEXT    NOT NULL,   -- the id inside the a_ token
    event_type         INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,   -- epoch s
    expires_at         INTEGER NOT NULL,   -- epoch s; 0 until the token is minted
    token_hash         BLOB    NOT NULL,   -- SHA-256; the plaintext is never stored
    token_prefix       TEXT    NOT NULL,
    revoked_at         INTEGER NOT NULL DEFAULT 0,
    status             INTEGER NOT NULL DEFAULT 2,  -- AgentSessionStatus
    requesting_address TEXT    NOT NULL DEFAULT '',
    approval_code      TEXT    NOT NULL DEFAULT '',
    approved_at        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_agent_session_event_log_session_id ON agent_session_event_log (session_id, id);
CREATE INDEX IF NOT EXISTS idx_agent_session_event_log_user_id ON agent_session_event_log (user_id, id);

CREATE TABLE IF NOT EXISTS user_session_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,   -- epoch ms
    author             INTEGER NOT NULL,
    session_id         TEXT    NOT NULL,   -- the id inside the u_ token
    event_type         INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update
    user_id            INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,   -- epoch s
    expires_at         INTEGER NOT NULL,   -- epoch s
    token_hash         BLOB    NOT NULL,   -- SHA-256; the plaintext is never stored
    revoked_at         INTEGER NOT NULL DEFAULT 0,
    kind               INTEGER NOT NULL DEFAULT 0,  -- UserSessionKind: 0 full, 1 bootstrap
    requesting_address TEXT    NOT NULL DEFAULT '',
    user_agent         TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_user_session_event_log_session_id ON user_session_event_log (session_id, id);
CREATE INDEX IF NOT EXISTS idx_user_session_event_log_user_id ON user_session_event_log (user_id, id);
