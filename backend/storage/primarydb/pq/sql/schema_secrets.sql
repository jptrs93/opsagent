-- Materialised view of the write log: one row per live keyslot, keyed by its
-- entity id, maintained by the reducer in pq/materialise.go. kind and node_id
-- are the wrapping: 1 = machine key of node_id, 2 = recovery code with
-- kdf_salt. The reducer copies the wrapped bytes as logged and
-- never opens them.
CREATE TABLE IF NOT EXISTS secret_keyslots (
    id          INTEGER PRIMARY KEY,
    kind        INTEGER NOT NULL,
    node_id     INTEGER NOT NULL,   -- machine slots: the node whose machine key wraps the SMK; 0 for the recovery slot
    smk_version INTEGER NOT NULL,   -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,   -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,   -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,               -- Argon2id salt (recovery slot only)
    seq         INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,   -- epoch ms
    author      INTEGER NOT NULL,
    created_time INTEGER NOT NULL,  -- epoch ms of the first row
    UNIQUE (kind, node_id)
);
