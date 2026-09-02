-- Global network override policies; see
-- docs/future-work/network-policy-implementation-plan.md. One append-only
-- event log per policy: every row carries the full content blob (delete rows
-- carry the last content), the highest-version row is the current state, and
-- event_type is the deletion truth. Policy ids are allocated as
-- MAX(policy_id)+1 under the service mutex; the log is never pruned, so ids
-- are never recycled: a rule referencing a deleted entity compiles to vacant
-- prefixes. Every event advances the global sequence in the same tx because
-- policies are cluster network map render inputs.

CREATE TABLE IF NOT EXISTS network_policy_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    created_time INTEGER NOT NULL,  -- epoch ms, first event's event_time
    author       INTEGER NOT NULL,
    policy_id    INTEGER NOT NULL,
    version      INTEGER NOT NULL,  -- top-level: bumps on every event
    data_blob    BLOB    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update / 3 delete
    UNIQUE (policy_id, version)
);
