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

-- Encrypted user secret values, keyed by plaintext name and immutable version
-- (e.g. 'staging.db.password' v1). The value is AEAD-sealed under the SMK with
-- the name bound as associated data, so rename must re-encrypt every version.
CREATE TABLE IF NOT EXISTS secrets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    smk_version  INTEGER NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);

-- Encrypted OpenDeploy-managed system secrets. These are not visible through
-- user-facing secret CRUD and use a separate AEAD associated-data class.
CREATE TABLE IF NOT EXISTS system_secrets (
    name        TEXT PRIMARY KEY,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    updated_at  INTEGER NOT NULL   -- epoch ms
);
