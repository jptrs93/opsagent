-- Size metadata allows S3-backed asset rows to list their original byte count
-- without loading the object. Existing inline rows backfill from blob length.
ALTER TABLE assets ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE assets SET size_bytes = length(blob) WHERE size_bytes = 0;
