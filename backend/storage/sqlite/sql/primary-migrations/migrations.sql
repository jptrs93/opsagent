-- todo future migrations

-- Node identifiers are immutable mTLS/deployment identities. Existing node
-- names were used for that purpose, so preserve their values during the rename.
ALTER TABLE nodes RENAME COLUMN sni TO identifier;
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_identifier ON nodes(identifier);
