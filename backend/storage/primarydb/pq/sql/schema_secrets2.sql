CREATE TABLE IF NOT EXISTS secret_displays (
    id           INTEGER PRIMARY KEY,
    space_id     INTEGER NOT NULL DEFAULT 1,
    name         TEXT    NOT NULL,
    directory_id INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL,
    updated_by   INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_displays_identity
    ON secret_displays(space_id, directory_id, name);
