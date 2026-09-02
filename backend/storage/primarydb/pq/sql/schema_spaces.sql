CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535),
    name TEXT    NOT NULL DEFAULT ''
);

INSERT INTO spaces (id, name) VALUES (0, '_system'), (1, 'global') ON CONFLICT(id) DO UPDATE SET name = excluded.name;
