-- Global network override policies; see
-- docs/future-work/network-policy-implementation-plan.md. Content lives in the
-- version log; the identity row carries only soft deletion. Ids are never
-- recycled: a rule referencing a deleted entity compiles to vacant prefixes.

CREATE TABLE IF NOT EXISTS network_policies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    deleted_at INTEGER NOT NULL DEFAULT 0  -- epoch ms, 0 = not deleted
);

CREATE TABLE IF NOT EXISTS network_policy_versions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_id  INTEGER NOT NULL,
    version    INTEGER NOT NULL,
    created_at INTEGER NOT NULL,             -- epoch ms
    author     INTEGER NOT NULL DEFAULT 0,   -- user id
    data_blob  BLOB    NOT NULL,
    global_seq INTEGER NOT NULL DEFAULT 0,
    UNIQUE (policy_id, version)
);
