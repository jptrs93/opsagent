-- Size metadata allows S3-backed asset rows to list their original byte count
-- without loading the object. Existing inline rows backfill from blob length.
ALTER TABLE assets ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE assets SET size_bytes = length(blob) WHERE size_bytes = 0;

-- Runner networking status fields are persisted as a compact protobuf blob so
-- endpoint/diagnostic schema can evolve without adding SQL columns per field.
ALTER TABLE deployment_status ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
ALTER TABLE deployment_status_history ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
