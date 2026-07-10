-- Current config + desired state for each deployment. One row per deployment,
-- keyed by an integer id auto-allocated on first insert. (space_id, machine,
-- name) is the human-readable identity and is unique; created_at is the
-- immutable first-seen time of that identity.
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    space_id        INTEGER NOT NULL DEFAULT 1,
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0,  -- epoch ms; first-seen time of this deployment identity
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',  -- the desired version should uniquely identify that version and not be re-used over time
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_configs_identity
    ON deployment_configs(space_id, machine, name);

CREATE TABLE IF NOT EXISTS spaces (
    id   INTEGER PRIMARY KEY,
    name TEXT    NOT NULL DEFAULT ''
);

INSERT INTO spaces (id, name) VALUES (0, 'opendeploy'), (1, 'default')
ON CONFLICT(id) DO UPDATE SET name = excluded.name;

-- Append-only log of every config mutation.
CREATE TABLE IF NOT EXISTS deployment_config_history (
    deployment_id   INTEGER NOT NULL,
    version         INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,  -- epoch ms
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (deployment_id, version)
);

-- Current deployment status. One mutable row per deployment.
CREATE TABLE IF NOT EXISTS deployment_status (
    deployment_id           INTEGER PRIMARY KEY,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds (0 = no status yet)
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x''
);

-- Append-only log of status transitions reported by the operator.
CREATE TABLE IF NOT EXISTS deployment_status_history (
    deployment_id           INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,  -- HLC clock, unix nanoseconds
    preparer_config_version INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_config_version   INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,  -- epoch ms
    runner_extra_blob       BLOB    NOT NULL DEFAULT x'',
    PRIMARY KEY (deployment_id, updated_at)
);

-- Auth: passkey users.
CREATE TABLE IF NOT EXISTS users (
    id        INTEGER PRIMARY KEY,
    name      TEXT    NOT NULL,
    data_blob BLOB   NOT NULL
);

-- Auth: JWT signing keys.
CREATE TABLE IF NOT EXISTS public_keys (
    kid       TEXT PRIMARY KEY,
    key_bytes BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS opendeploy_config (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,
    config_blob BLOB    NOT NULL
);

-- Plain user-managed configuration values. These are intentionally not
-- encrypted at rest; encrypted credentials belong in secrets instead.
CREATE TABLE IF NOT EXISTS user_configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    value        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);

-- Versioned user-managed file assets. Rows are immutable: editing an asset
-- appends a new version for the same key. Small assets are stored inline in
-- blob; large assets store their object reference in location.
CREATE TABLE IF NOT EXISTS assets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT    NOT NULL,
    space_id    INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,  -- epoch ms; creation time of this version
    version     INTEGER NOT NULL,
    format      TEXT    NOT NULL DEFAULT '',
    location    TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    blob        BLOB    NOT NULL,
    UNIQUE (key, version)
);

CREATE INDEX IF NOT EXISTS idx_assets_key_created_at
    ON assets(key, created_at);

-- Secrets: envelope-encrypted key/value store. PRIMARY-ONLY — these two tables
-- are never replicated to secondaries (the cluster feeder only sends deployment
-- configs/status; see primary/session.go). They are created on every node by
-- the shared schema but stay empty off the primary.

-- Wrapped copies of the Secrets Master Key (SMK), one row per unlock method
-- ("slot"). The SMK encrypts every secret value; each slot stores the SMK
-- sealed under a different key-encryption key (the machine key, the recovery
-- code, ...). Adding/rotating a slot never re-encrypts the secrets themselves.
CREATE TABLE IF NOT EXISTS secret_keyslots (
    slot        TEXT PRIMARY KEY,         -- 'machine' | 'recovery'
    smk_version INTEGER NOT NULL,         -- which SMK generation this wraps
    wrapped_smk BLOB    NOT NULL,         -- SMK sealed under this slot's KEK
    nonce       BLOB    NOT NULL,         -- AEAD nonce for wrapped_smk
    kdf_salt    BLOB,                     -- Argon2id salt (recovery slot only)
    created_at  INTEGER NOT NULL          -- epoch ms
);

-- Encrypted user secret values, keyed by plaintext name and immutable version
-- (e.g. 'staging.db.password' v1). The value is AEAD-sealed under the SMK with
-- the name bound as associated data, so rename must re-encrypt every version.
CREATE TABLE IF NOT EXISTS secrets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    smk_version  INTEGER NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);

-- Encrypted OpenDeploy-managed system secrets. These are not visible through
-- user-facing secret CRUD and use a separate AEAD associated-data class.
CREATE TABLE IF NOT EXISTS system_secrets (
    name        TEXT PRIMARY KEY,
    smk_version INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,  -- epoch ms
    updated_at  INTEGER NOT NULL   -- epoch ms
);

-- Small machine-local key/value state, e.g. the worker's cached cluster network
-- parameters (programmed into the dataplane on boot before the primary is
-- reachable). Created on every node; never replicated.
CREATE TABLE IF NOT EXISTS local_kv (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
);

-- Worker enrollment requests. A reconnecting unenrolled worker is identified by
-- requesting_machine_id and updates its existing row back to waiting.
CREATE TABLE IF NOT EXISTS enrollment_requests (
    id                       INTEGER PRIMARY KEY,
    created_at               INTEGER NOT NULL,  -- epoch ms
    updated_at               INTEGER NOT NULL,  -- epoch ms
    requesting_ip_address    TEXT    NOT NULL DEFAULT '',
    requesting_machine_id    TEXT    NOT NULL,
    opendeploy_version       TEXT    NOT NULL DEFAULT '',
    status                   TEXT    NOT NULL DEFAULT 'waiting'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_requests_machine_id
    ON enrollment_requests(requesting_machine_id);

-- Canonical cluster node registry. The primary is inserted with enrollment_id
-- NULL; enrolled workers reference the enrollment request that accepted them.
CREATE TABLE IF NOT EXISTS nodes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    enrollment_id INTEGER,
    name          TEXT    NOT NULL,
    sni           TEXT    NOT NULL DEFAULT '',
    roles         TEXT    NOT NULL DEFAULT '[]', -- JSON array of integer role ids
    wg_public_key TEXT    NOT NULL DEFAULT '',
    UNIQUE(name),
    UNIQUE(enrollment_id)
);
