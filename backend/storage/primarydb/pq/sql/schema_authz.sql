-- Materialised views of the write log for access control, maintained by the
-- reducer in pq/materialise.go; see backend/lib/authz for the rule model.
-- seq, event_time, and author are the envelope of the last write.
CREATE TABLE IF NOT EXISTS authz_grant_templates (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    builtin      INTEGER NOT NULL,
    data_blob    BLOB    NOT NULL,  -- AuthzGrantTemplateSpec
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS authz_grants (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL,
    template_id  INTEGER NOT NULL,  -- 0 for a grant that carries its own rule
    data_blob    BLOB    NOT NULL,  -- AuthzGrantSource
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_authz_grants_user ON authz_grants (user_id, id);

CREATE TABLE IF NOT EXISTS authz_global_rules (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    data_blob    BLOB    NOT NULL,  -- AuthzRule
    created_time INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL
);
