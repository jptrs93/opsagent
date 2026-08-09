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
