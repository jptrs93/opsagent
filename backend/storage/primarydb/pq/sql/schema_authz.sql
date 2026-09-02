-- rules; see backend/lib/authz.
CREATE TABLE IF NOT EXISTS public_keys (
   kid       TEXT PRIMARY KEY,
   key_bytes BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS authz_rule_template_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    created_time INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author       INTEGER NOT NULL,
    template_id  INTEGER NOT NULL,
    version      INTEGER NOT NULL,  -- top-level: bumps on every event
    name         TEXT    NOT NULL,
    builtin      INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (template_id, version)
);

CREATE TABLE IF NOT EXISTS authz_grant_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    created_time INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author       INTEGER NOT NULL,
    grant_id     INTEGER NOT NULL,
    version      INTEGER NOT NULL,  -- top-level: bumps on every event
    user_id      INTEGER NOT NULL,
    template_id  INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (grant_id, version)
);

CREATE TABLE IF NOT EXISTS global_access_rule_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    created_time INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author       INTEGER NOT NULL,
    rule_id      INTEGER NOT NULL,
    version      INTEGER NOT NULL,  -- top-level: bumps on every event
    name         TEXT    NOT NULL,
    disabled     INTEGER NOT NULL DEFAULT 0,  -- reserved, not yet wired
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (rule_id, version)
);
