-- Operator requests to reseed one repository's Nix build store on every node.
-- Append-only: one row per request, live state is the newest row per repo.
CREATE TABLE IF NOT EXISTS nix_store_reset_event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq   INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL,
    repo         TEXT    NOT NULL,
    event_type   INTEGER NOT NULL,  -- AuthzVerb value: 1 create / 2 update
    requested_at INTEGER NOT NULL   -- epoch ms
);

CREATE INDEX IF NOT EXISTS idx_nix_store_reset_event_log_repo ON nix_store_reset_event_log (repo, id);
