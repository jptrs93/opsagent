-- Authz: additive access grants evaluated against rule templates and global
-- deny rules; see backend/lib/authz. Rows are single mutable/immutable records
-- with no version history — templates are edited in place and grants resolve
-- them live at evaluation time.

CREATE TABLE IF NOT EXISTS authz_rule_templates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    builtin    INTEGER NOT NULL DEFAULT 0,
    deleted    INTEGER NOT NULL DEFAULT 0,
    created_by INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0,
    data_blob  BLOB    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_authz_rule_templates_name
    ON authz_rule_templates (name) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS authz_grants (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    template_id INTEGER NOT NULL DEFAULT 0,
    created_by  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT 0,
    data_blob   BLOB    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_authz_grants_user
    ON authz_grants (user_id);

CREATE INDEX IF NOT EXISTS idx_authz_grants_template
    ON authz_grants (template_id);

CREATE TABLE IF NOT EXISTS global_access_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    created_by INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0,
    data_blob  BLOB    NOT NULL
);
