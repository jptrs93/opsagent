-- Operator requests to reseed one repository's Nix build store on every node:
-- a materialised view of the write log with one row per repository, the
-- newest request, maintained by the reducer in pq/materialise.go.
CREATE TABLE IF NOT EXISTS nix_store_resets (
    id           INTEGER PRIMARY KEY,
    repo         TEXT    NOT NULL UNIQUE,
    requested_at INTEGER NOT NULL,  -- epoch ms
    seq          INTEGER NOT NULL,
    event_time   INTEGER NOT NULL,  -- epoch ms
    author       INTEGER NOT NULL,
    created_time INTEGER NOT NULL   -- epoch ms of the first row
);
