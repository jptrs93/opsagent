CREATE TABLE IF NOT EXISTS secret_keyslots (
    slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
    smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
    created_at  INTEGER NOT NULL          -- epoch ms
);

CREATE TABLE IF NOT EXISTS secrets (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at         INTEGER NOT NULL,            -- epoch ms
    deleted_at         INTEGER NOT NULL DEFAULT 0   -- epoch ms, 0 = not deleted
);

-- Append-only log of space assignments; the newest row is the secret's current
-- space. Creation writes the first row.
CREATE TABLE IF NOT EXISTS secret_spaces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    secret_id   INTEGER NOT NULL,
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    created_at  INTEGER NOT NULL,            -- epoch ms
    space_id    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_secret_spaces_secret ON secret_spaces (secret_id);

-- ciphertext is AEAD-sealed under the SMK with
-- "opendeploy-secret:user:s<secret_id>:v<version>" as associated data, so it
-- cannot be moved to another secret or version.
CREATE TABLE IF NOT EXISTS secret_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    secret_id   INTEGER NOT NULL,
    version     INTEGER NOT NULL,  -- version can be derived but is kept for convenience and to make future pruning possible.
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,            -- epoch ms
    author  INTEGER NOT NULL DEFAULT 0,  -- user id
    UNIQUE (secret_id, version)
);

-- OpenDeploy-managed system secrets: not visible through user-facing secret
-- CRUD, outside the secrets/configs file system, and sealed under a separate
-- (still name-bound) associated-data class.
CREATE TABLE IF NOT EXISTS system_secrets (
    name        TEXT PRIMARY KEY,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    updated_at  INTEGER NOT NULL   -- epoch ms
);
