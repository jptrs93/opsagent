-- Plain user-managed configuration values. These are intentionally not
-- encrypted at rest; encrypted credentials belong in secrets instead.
--
-- Configs and secrets share ONE file system per space (see schema_secrets.sql):
-- a name must be unique among sibling configs, secrets, and value_directories
-- under the same parent, enforced by the storage layer's value namespace mutex.

-- The shared directory tree for secrets and configs. One tree per space.
CREATE TABLE IF NOT EXISTS value_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    space_id    INTEGER NOT NULL DEFAULT 1,
    name        TEXT    NOT NULL,
    parent_id   INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at  INTEGER NOT NULL,            -- epoch ms
    created_by  INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system
);

-- Stable config identities. configs.id survives renames, moves, and new
-- versions; the values live in immutable config_versions rows.
CREATE TABLE IF NOT EXISTS configs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    space_id           INTEGER NOT NULL DEFAULT 1,
    value_directory_id INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at         INTEGER NOT NULL,            -- epoch ms
    created_by         INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system
);

-- Immutable versions. Deployment env refs and system settings pin
-- config_versions.id.
CREATE TABLE IF NOT EXISTS config_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id   INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    value       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,            -- epoch ms
    created_by  INTEGER NOT NULL DEFAULT 0,  -- user id; 0 = unknown/system
    UNIQUE (config_id, version)
);
