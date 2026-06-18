-- Primary storage migrations. Re-run on every startup. Already-applied ADD COLUMN
-- statements are ignored by init.go.

ALTER TABLE enrollment_requests ADD COLUMN opendeploy_version TEXT NOT NULL DEFAULT '';

ALTER TABLE user_configs ADD COLUMN id INTEGER NOT NULL DEFAULT 0;
UPDATE user_configs SET id = rowid WHERE id = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_configs_id ON user_configs(id);

ALTER TABLE secrets ADD COLUMN id INTEGER NOT NULL DEFAULT 0;
UPDATE secrets SET id = rowid WHERE id = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_id ON secrets(id);
