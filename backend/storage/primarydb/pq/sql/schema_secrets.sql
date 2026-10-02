-- Materialised view of the write log: one row per live keyslot, keyed by the
-- stream entity id node_id * 256 + kind, maintained by the reducer in
-- pq/materialise.go. The reducer copies the wrapped bytes as logged and
-- never opens them.
CREATE TABLE IF NOT EXISTS secret_keyslots (
    id          INTEGER PRIMARY KEY,
    kind        INTEGER NOT NULL,   -- SecretKeyslotKind: 1 machine / 2 recovery
    node_id     INTEGER NOT NULL,   -- machine slots: the node whose machine key wraps the SMK; 0 for the recovery slot
    smk_version INTEGER NOT NULL,   -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,   -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,   -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,               -- Argon2id salt (recovery slot only)
    updated_at  INTEGER NOT NULL,   -- epoch ms, the slot's own clock
    seq         INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,   -- epoch ms
    author      INTEGER NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms of the first row
    UNIQUE (kind, node_id)
);
