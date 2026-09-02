CREATE TABLE IF NOT EXISTS secret_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- value rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    created_time       INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author             INTEGER NOT NULL,
    secret_id          INTEGER NOT NULL,
    version            INTEGER NOT NULL,  -- top-level: bumps on every event
    value_version      INTEGER NOT NULL,  -- AAD-bound; bumps only on value writes
    space_version      INTEGER NOT NULL,  -- bumps only on space moves
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    smk_version        INTEGER,           -- NULL unless this event writes the value
    ciphertext         BLOB,
    nonce              BLOB,
    event_type         INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (secret_id, version)
);

CREATE TABLE IF NOT EXISTS secret_keyslots (
   slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
   smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
   wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
   nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
   kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
   created_at  INTEGER NOT NULL          -- epoch ms
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
