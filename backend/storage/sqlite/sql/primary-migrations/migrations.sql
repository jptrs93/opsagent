-- Primary storage migrations. Re-run on every startup. Already-applied ADD COLUMN
-- statements are ignored by init.go.

ALTER TABLE enrollment_requests ADD COLUMN opendeploy_version TEXT NOT NULL DEFAULT '';
