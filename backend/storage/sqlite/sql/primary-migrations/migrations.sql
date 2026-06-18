-- Primary storage migrations. Re-run on every startup. Already-applied ADD COLUMN
-- statements are ignored by init.go.

ALTER TABLE enrollment_requests ADD COLUMN opendeploy_version TEXT NOT NULL DEFAULT '';

ALTER TABLE user_configs ADD COLUMN id INTEGER NOT NULL DEFAULT 0;
UPDATE user_configs SET id = rowid WHERE id = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_configs_id ON user_configs(id);

ALTER TABLE secrets ADD COLUMN id INTEGER NOT NULL DEFAULT 0;
UPDATE secrets SET id = rowid WHERE id = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_id ON secrets(id);

CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY,
    name TEXT    NOT NULL DEFAULT ''
);
INSERT INTO spaces (id, name) VALUES (0, 'opendeploy'), (1, 'default')
ON CONFLICT(id) DO UPDATE SET name = excluded.name;

DROP INDEX IF EXISTS idx_deployment_configs_identity;
ALTER TABLE deployment_configs DROP COLUMN environment;
ALTER TABLE deployment_configs ADD COLUMN space_id INTEGER NOT NULL DEFAULT 1;
UPDATE deployment_configs SET space_id = 0 WHERE name = 'opendeploy';
CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_identity
    ON deployment_configs(space_id, machine, name);

ALTER TABLE assets ADD COLUMN space_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE user_configs ADD COLUMN space_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE secrets ADD COLUMN space_id INTEGER NOT NULL DEFAULT 1;
