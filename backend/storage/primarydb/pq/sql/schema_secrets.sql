-- Secrets: envelope-encrypted key/value store. PRIMARY-ONLY — these tables are
-- never replicated to secondaries (the cluster feeder only sends deployment
-- configs/status; see primary/session.go). They are created on every node by
-- the shared schema but stay empty off the primary.

-- Wrapped copies of the Secrets Master Key (SMK), one row per unlock method
-- ("slot"). The SMK encrypts every secret value; each slot stores the SMK
-- sealed under a different key-encryption key (the machine key, the recovery
-- code, ...). Adding/rotating a slot never re-encrypts the secrets themselves.
CREATE TABLE IF NOT EXISTS secret_keyslots (
    slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
    smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
    created_at  INTEGER NOT NULL          -- epoch ms
);

-- Secrets and configs share ONE file system per space: a name must be unique
-- among sibling secrets, configs, and value_directories under the same parent.
-- That law spans three tables, so it cannot be a SQL constraint — every
-- create/rename/move MUST go through the storage layer's value namespace
-- mutex, which checks all three. Directory rows for that shared tree live in
-- value_directories (see schema_configs.sql).

-- Stable secret identities. secrets.id survives renames, moves, and new
-- versions; the encrypted content lives in immutable secret_versions rows.
CREATE TABLE IF NOT EXISTS secrets (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    space_id           INTEGER NOT NULL DEFAULT 1,
    value_directory_id INTEGER NOT NULL DEFAULT 0,  -- 0 = the implicit root
    created_at         INTEGER NOT NULL,            -- epoch ms
    created_by         INTEGER NOT NULL DEFAULT 0   -- user id; 0 = unknown/system, negative = agent of user -created_by
);

-- Immutable encrypted versions. Deployment env refs and system settings pin
-- secret_versions.id. The value is AEAD-sealed under the SMK with
-- "opendeploy-secret:user:s<secret_id>:v<version>" as associated data, so a
-- ciphertext cannot be moved to another secret or version. (Rows written
-- before the identity split carry the legacy name-bound AAD until the
-- Manager's re-seal sweep converts them at first unlock.)
CREATE TABLE IF NOT EXISTS secret_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    secret_id   INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,            -- epoch ms
    created_by  INTEGER NOT NULL DEFAULT 0,  -- user id; 0 = unknown/system, negative = agent of user -created_by
    UNIQUE (secret_id, version)
);

-- Encrypted OpenDeploy-managed system secrets. These are not visible through
-- user-facing secret CRUD, sit outside the secrets/configs file system, and
-- use a separate AEAD associated-data class (still name-bound).
CREATE TABLE IF NOT EXISTS system_secrets (
    name        TEXT PRIMARY KEY,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    updated_at  INTEGER NOT NULL   -- epoch ms
);
