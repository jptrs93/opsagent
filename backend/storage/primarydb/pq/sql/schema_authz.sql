-- rules; see backend/lib/authz.

CREATE TABLE IF NOT EXISTS authz_rule_templates (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    name    TEXT    NOT NULL,
    builtin INTEGER NOT NULL DEFAULT 0,
    deleted_at INTEGER NOT NULL DEFAULT 0  -- epoch ms, 0 = not deleted
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_authz_rule_templates_name
    ON authz_rule_templates (name) WHERE deleted_at = 0;

CREATE TABLE IF NOT EXISTS authz_rule_template_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL,
    version     INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    created_at  INTEGER NOT NULL,  -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    data_blob   BLOB    NOT NULL,
    global_seq  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (template_id, version)
);

-- Grant content is immutable: access changes are revoke + re-grant, so there
-- is no version log. Revocation soft-deletes the row.
CREATE TABLE IF NOT EXISTS authz_grants (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    template_id INTEGER NOT NULL DEFAULT 0,
    deleted_at  INTEGER NOT NULL DEFAULT 0,  -- epoch ms, 0 = not deleted
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    created_at  INTEGER NOT NULL DEFAULT 0,  -- epoch ms
    data_blob   BLOB    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_authz_grants_user
    ON authz_grants (user_id);

CREATE INDEX IF NOT EXISTS idx_authz_grants_template
    ON authz_grants (template_id);

CREATE TABLE IF NOT EXISTS global_access_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    author INTEGER NOT NULL DEFAULT 0,  -- user id
    created_at INTEGER NOT NULL DEFAULT 0,  -- epoch ms
    data_blob  BLOB    NOT NULL
);
