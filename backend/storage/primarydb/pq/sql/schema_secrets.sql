CREATE TABLE IF NOT EXISTS secret_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,  -- value rows: the pinnable version id
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,  -- epoch ms
    created_time       INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author             INTEGER NOT NULL,
    secret_id          INTEGER NOT NULL,
    version            INTEGER NOT NULL,  -- top-level: bumps on every event
    value_version      INTEGER NOT NULL,  -- bumps only on value writes
    value_changed      INTEGER NOT NULL DEFAULT 0,  -- 1 iff this event bumped value_version
    name               TEXT    NOT NULL,
    value_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    smk_version        INTEGER NOT NULL,  -- current sealed value's SMK generation, carried forward on non-value events
    ciphertext         BLOB    NOT NULL,  -- current sealed value, carried forward on non-value events
    nonce              BLOB    NOT NULL,
    event_type         INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (secret_id, version)
);

-- A value reference (secret_id, value_version) names exactly one value-changing row.
CREATE UNIQUE INDEX IF NOT EXISTS secret_value_versions
    ON secret_event_log (secret_id, value_version) WHERE value_changed != 0;

CREATE TABLE IF NOT EXISTS secret_keyslot_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,   -- epoch ms
    author      INTEGER NOT NULL,   -- user id; 0 system, negative = agent of user -author
    kind        INTEGER NOT NULL,   -- SecretKeyslotKind: 1 machine / 2 recovery
    node_id     INTEGER NOT NULL,   -- machine slots: the node whose machine key wraps the SMK; 0 for the recovery slot
    event_type  INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update / 3 delete
    smk_version INTEGER NOT NULL,   -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,   -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,   -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB                -- Argon2id salt (recovery slot only)
);

CREATE INDEX IF NOT EXISTS idx_secret_keyslot_event_log_key ON secret_keyslot_event_log (kind, node_id, id);
