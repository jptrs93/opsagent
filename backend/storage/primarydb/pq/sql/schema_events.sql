CREATE TABLE IF NOT EXISTS events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    author_id   INTEGER NOT NULL DEFAULT 0,
    entity_type INTEGER NOT NULL,
    entity_id   INTEGER NOT NULL,
    action      INTEGER NOT NULL,
    blob        BLOB    NOT NULL DEFAULT x''
);

CREATE INDEX IF NOT EXISTS idx_events_entity
    ON events(entity_type, entity_id, seq);
