-- Operator requests to reseed one repository's Nix build store on every node.
-- One row per repository; a new request replaces the previous timestamp.
CREATE TABLE IF NOT EXISTS nix_store_resets (
    repo         TEXT    PRIMARY KEY,
    requested_at INTEGER NOT NULL  -- epoch ms
);
