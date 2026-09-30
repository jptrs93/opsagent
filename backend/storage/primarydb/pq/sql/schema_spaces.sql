-- Append-only: one row per space event. Live state is the newest row per
-- space_id whose event_type is not delete. Space ids are never reused.
CREATE TABLE IF NOT EXISTS space_event_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq  INTEGER NOT NULL,
    event_time  INTEGER NOT NULL,   -- epoch ms
    author      INTEGER NOT NULL,   -- user id; 0 system, negative = agent of user -author
    space_id    INTEGER NOT NULL CHECK (space_id BETWEEN 0 AND 65535),
    event_type  INTEGER NOT NULL,   -- AuthzVerb value: 1 create / 2 update / 3 delete
    name        TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_space_event_log_space_id ON space_event_log (space_id, id);

INSERT INTO space_event_log (global_seq, event_time, author, space_id, event_type, name)
SELECT 0, 0, 0, 0, 1, '_system' WHERE NOT EXISTS (SELECT 1 FROM space_event_log WHERE space_id = 0);
INSERT INTO space_event_log (global_seq, event_time, author, space_id, event_type, name)
SELECT 0, 0, 0, 1, 1, 'global' WHERE NOT EXISTS (SELECT 1 FROM space_event_log WHERE space_id = 1);

CREATE INDEX IF NOT EXISTS idx_space_event_log_seq ON space_event_log (global_seq, id);
