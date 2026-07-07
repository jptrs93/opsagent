-- Runner networking status fields are persisted as a compact protobuf blob so
-- endpoint/diagnostic schema can evolve without adding SQL columns per field.
ALTER TABLE deployment_status ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
ALTER TABLE deployment_status_history ADD COLUMN runner_extra_blob BLOB NOT NULL DEFAULT x'';
