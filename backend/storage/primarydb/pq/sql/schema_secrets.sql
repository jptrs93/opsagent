CREATE TABLE IF NOT EXISTS secret_keyslots (
    slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
    smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
    created_at  INTEGER NOT NULL          -- epoch ms
);

-- One append-only event log per secret, one row per event; version 1 is the
-- creation write. Identity facets (name, directory, space) are denormalised
-- onto every row, so the highest-version row is the complete current state
-- with event_type as the deletion truth; created_time is the first event's
-- event_time copied to every row. value_version bumps only on value writes
-- and space_version only on space moves. The sealed payload is present only
-- on rows that write the value and NULL otherwise — a row with a non-NULL
-- ciphertext is a pinnable value version and its id is what deployment env
-- refs and settings pin. The ciphertext is AEAD-sealed under the SMK with
-- "opendeploy-secret:user:s<secret_id>:v<value_version>" as associated data,
-- so it cannot be moved to another secret or version, and renames/moves never
-- re-encrypt. Secret ids are allocated as MAX(secret_id)+1 under the service
-- mutex; the log is never pruned, so neither secret ids (AAD-bound) nor
-- pinned version-row ids are reused.
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
