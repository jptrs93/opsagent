-- Current config + desired state for each deployment. One row per deployment,
-- keyed by an integer id auto-allocated on first insert. (environment, machine,
-- name) is the human-readable identity and is unique; created_at is the
-- immutable first-seen time of that identity.
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
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
    ON deployment_configs(environment, machine, name);

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
    runner_last_restart_at  INTEGER  -- epoch ms
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

-- Auth: singleton settings. Absence of this row means the runtime may still use
-- the install-time OPENDEPLOY_INITIAL_MASTER_PASSWORD_HASH fallback. Once this
-- row exists, it is authoritative and the env fallback is ignored.
CREATE TABLE IF NOT EXISTS auth_settings (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    master_password_hash TEXT    NOT NULL DEFAULT ''
);

-- Runtime system configuration values. Absence of a row means the corresponding
-- OPENDEPLOY_INITIAL_* or install-time default is still in effect.
CREATE TABLE IF NOT EXISTS system_config (
    key        TEXT PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT 0  -- epoch ms
);

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

-- Encrypted user secret values, keyed by a plaintext name (e.g. 'staging.db.password').
-- The value is AEAD-sealed under the SMK with the name bound as associated data
-- so a row cannot be moved to a different name. secret_group is optional and
-- reserved for grouping secrets in the UI later; it carries no security meaning.
CREATE TABLE IF NOT EXISTS secrets (
    name         TEXT PRIMARY KEY,
    secret_group TEXT    NOT NULL DEFAULT '',
    smk_version  INTEGER NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,  -- epoch ms
    updated_at   INTEGER NOT NULL,  -- epoch ms
    updated_by   INTEGER NOT NULL DEFAULT 0
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

-- Worker enrollment requests. A reconnecting unenrolled worker is identified by
-- requesting_machine_id and updates its existing row back to waiting.
CREATE TABLE IF NOT EXISTS enrollment_requests (
    id                       INTEGER PRIMARY KEY,
    created_at               INTEGER NOT NULL,  -- epoch ms
    updated_at               INTEGER NOT NULL,  -- epoch ms
    requesting_ip_address    TEXT    NOT NULL DEFAULT '',
    requesting_machine_id    TEXT    NOT NULL,
    status                   TEXT    NOT NULL DEFAULT 'waiting'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_requests_machine_id
    ON enrollment_requests(requesting_machine_id);
