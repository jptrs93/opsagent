-- name: NextGlobalSeq :one
UPDATE global_seq SET value = value + 1 WHERE id = 1
RETURNING value;

-- name: GetGlobalSeq :one
SELECT value FROM global_seq WHERE id = 1;

-- name: ListSpaces :many
SELECT id, name FROM spaces ORDER BY id;

-- name: CreateSpace :one
INSERT INTO spaces (name) VALUES (?)
RETURNING id, name;

-- name: UpdateSpace :one
UPDATE spaces SET name = ? WHERE id = ?
RETURNING id, name;

-- name: DeleteSpace :exec
DELETE FROM spaces WHERE id = ?;

-- Reads are hand-written in scheduled_instances.go: an instance row is its
-- identity joined with the version log for creation time and current state.
-- Creation appends the v1 version row in the same tx; state transitions are
-- pure appends.

-- name: InsertScheduledInstance :one
INSERT INTO scheduled_instances (deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: AppendScheduledInstanceVersion :exec
INSERT INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state, global_seq)
SELECT @scheduled_instance_id, COALESCE(MAX(version), 0) + 1, @created_at, @state, @global_seq
FROM scheduled_instance_versions
WHERE scheduled_instance_id = @scheduled_instance_id;

-- name: InsertScheduledInstanceStatus :exec
INSERT INTO scheduled_instance_status (
    scheduled_instance_id, updated_at, deployment_id,
    preparer_spec_version, preparer_artifact,
    preparer_inputs_status, preparer_image_status,
    runner_spec_version, runner_pid, runner_artifact, runner_status,
    runner_num_restarts, runner_last_restart_at, runner_extra_blob,
    runner_exit_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scheduled_instance_id, updated_at) DO UPDATE SET
    deployment_id = excluded.deployment_id,
    preparer_spec_version = excluded.preparer_spec_version,
    preparer_artifact = excluded.preparer_artifact,
    preparer_inputs_status = excluded.preparer_inputs_status,
    preparer_image_status = excluded.preparer_image_status,
    runner_spec_version = excluded.runner_spec_version,
    runner_pid = excluded.runner_pid,
    runner_artifact = excluded.runner_artifact,
    runner_status = excluded.runner_status,
    runner_num_restarts = excluded.runner_num_restarts,
    runner_last_restart_at = excluded.runner_last_restart_at,
    runner_extra_blob = excluded.runner_extra_blob,
    runner_exit_code = excluded.runner_exit_code;

-- name: ListLatestScheduledInstanceStatuses :many
SELECT s.scheduled_instance_id, s.updated_at, s.deployment_id,
       s.preparer_spec_version, s.preparer_artifact,
       s.preparer_inputs_status, s.preparer_image_status,
       s.runner_spec_version, s.runner_pid, s.runner_artifact, s.runner_status,
       s.runner_num_restarts, s.runner_last_restart_at, s.runner_extra_blob,
       s.runner_exit_code
FROM scheduled_instance_status s
JOIN (
    SELECT scheduled_instance_id, MAX(updated_at) AS updated_at
    FROM scheduled_instance_status
    GROUP BY scheduled_instance_id
) latest ON latest.scheduled_instance_id = s.scheduled_instance_id AND latest.updated_at = s.updated_at;

-- name: ListScheduledInstanceStatusHistorySince :many
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_spec_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_spec_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob,
       runner_exit_code
FROM scheduled_instance_status
WHERE scheduled_instance_id = ? AND updated_at > ?
ORDER BY updated_at ASC;

-- name: ListScheduledInstanceStatusHistoryForDeployment :many
SELECT scheduled_instance_id, updated_at, deployment_id,
       preparer_spec_version, preparer_artifact,
       preparer_inputs_status, preparer_image_status,
       runner_spec_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob,
       runner_exit_code
FROM scheduled_instance_status
WHERE deployment_id = ?
ORDER BY updated_at ASC;

-- name: NextDeploymentID :one
SELECT COALESCE(MAX(deployment_id), 0) + 1 FROM deployment_event_log;

-- name: GetDeploymentEventByVersion :one
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.deployment_id,
       e.version, e.spec_version, e.space_assignment_version, e.name_version,
       e.value, e.event_type
FROM deployment_event_log e
WHERE e.deployment_id = ? AND e.version = ?;

-- name: GetLatestDeploymentEvent :one
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.deployment_id,
       e.version, e.spec_version, e.space_assignment_version, e.name_version,
       e.value, e.event_type
FROM deployment_event_log e
WHERE e.deployment_id = ?
ORDER BY e.version DESC LIMIT 1;

-- name: ListLatestDeploymentEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.deployment_id,
       e.version, e.spec_version, e.space_assignment_version, e.name_version,
       e.value, e.event_type
FROM deployment_event_log e
JOIN (SELECT deployment_id, MAX(version) AS version
      FROM deployment_event_log GROUP BY deployment_id) latest
  ON latest.deployment_id = e.deployment_id AND latest.version = e.version
ORDER BY e.deployment_id;

-- name: ListDeletedDeploymentEvents :many
SELECT id, global_seq, event_time, created_time, author, deployment_id,
       version, spec_version, space_assignment_version, name_version,
       value, event_type
FROM deployment_event_log
WHERE event_type = ?
ORDER BY event_time DESC, deployment_id DESC;

-- name: ListDeploymentEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.deployment_id,
       e.version, e.spec_version, e.space_assignment_version, e.name_version,
       e.value, e.event_type
FROM deployment_event_log e
WHERE e.deployment_id = ?
ORDER BY e.version ASC;

-- name: GetDeploymentEventBySpecVersion :one
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.deployment_id,
       e.version, e.spec_version, e.space_assignment_version, e.name_version,
       e.value, e.event_type
FROM deployment_event_log e
WHERE e.deployment_id = ? AND e.spec_version = ?
ORDER BY e.version ASC LIMIT 1;

-- name: GetUser :one
SELECT id, name, data_blob, created_at, last_login_at FROM users WHERE id = ?;

-- UpsertUser deliberately leaves created_at and last_login_at alone on
-- conflict: the same call both creates users and rewrites their credential
-- blob later.
-- name: UpsertUser :exec
INSERT INTO users (id, name, data_blob, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, data_blob = excluded.data_blob;

-- name: TouchUserLastLogin :exec
UPDATE users SET last_login_at = ? WHERE id = ?;

-- name: ListUsers :many
SELECT id, name, data_blob, created_at, last_login_at FROM users ORDER BY id;

-- name: InsertAgentSession :exec
INSERT INTO agent_sessions (id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
                            status, requesting_address, approval_code, approved_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?);

-- name: GetAgentSession :one
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE id = ?;

-- name: ListAgentSessionsForUser :many
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListPendingAgentSessionsForUser :many
SELECT id, user_id, created_at, expires_at, token_hash, token_prefix, revoked_at, scopes,
       status, requesting_address, approval_code, approved_at
FROM agent_sessions WHERE user_id = ? AND status = 1 ORDER BY created_at DESC;

-- name: SetAgentSessionStatus :exec
UPDATE agent_sessions SET status = ?, approved_at = ?, revoked_at = ?
WHERE id = ?;

-- ApproveAgentSession records the approver's scopes and the approval together,
-- and only from PENDING, so two operators racing on the same request cannot
-- both approve it and the second one's scopes cannot overwrite the first's.
-- name: ApproveAgentSession :execrows
UPDATE agent_sessions SET status = 2, approved_at = ?, scopes = ?
WHERE id = ? AND user_id = ? AND status = 1;

-- ClaimAgentSessionToken mints a session's token exactly once. The empty
-- token_hash in the WHERE clause is the guard: two concurrent pickups race here
-- and only the one that finds the row unclaimed reports a row affected, so a
-- session can never hand out two working tokens.
-- name: ClaimAgentSessionToken :execrows
UPDATE agent_sessions SET token_hash = ?, token_prefix = ?, expires_at = ?, scopes = ?
WHERE id = ? AND status = 2 AND length(token_hash) = 0;

-- name: RevokeAgentSession :exec
UPDATE agent_sessions SET revoked_at = ?, status = ?
WHERE id = ? AND user_id = ? AND revoked_at = 0;

-- name: InsertPersonalSession :exec
INSERT INTO personal_sessions (id, user_id, created_at, expires_at, token_hash, revoked_at,
                               requesting_address, user_agent, last_active_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?);

-- name: GetPersonalSession :one
SELECT id, user_id, created_at, expires_at, token_hash, revoked_at,
       requesting_address, user_agent, last_active_at
FROM personal_sessions WHERE id = ?;

-- name: ListPersonalSessionsForUser :many
SELECT id, user_id, created_at, expires_at, token_hash, revoked_at,
       requesting_address, user_agent, last_active_at
FROM personal_sessions WHERE user_id = ? ORDER BY created_at DESC;

-- name: RevokePersonalSession :execrows
UPDATE personal_sessions SET revoked_at = ?
WHERE id = ? AND user_id = ? AND revoked_at = 0;

-- name: TouchPersonalSessionActivity :exec
UPDATE personal_sessions SET last_active_at = ? WHERE id = ?;

-- name: GetPublicKey :one
SELECT kid, key_bytes FROM public_keys WHERE kid = ?;

-- name: UpsertPublicKey :exec
INSERT INTO public_keys (kid, key_bytes) VALUES (?, ?)
ON CONFLICT(kid) DO UPDATE SET key_bytes = excluded.key_bytes;

-- Container reads are hand-written in values.go: the current space is the
-- newest config_spaces row and reads exclude soft-deleted configs.

-- name: InsertConfigRow :one
INSERT INTO configs (name, value_directory_id, created_at)
VALUES (?, ?, ?)
RETURNING id;

-- name: InsertConfigSpaceRow :exec
INSERT INTO config_spaces (config_id, author, created_at, space_id, global_seq)
VALUES (?, ?, ?, ?, ?);

-- name: ListConfigSpaceRowsByConfigID :many
SELECT id, config_id, author, created_at, space_id, global_seq
FROM config_spaces WHERE config_id = ?
ORDER BY id ASC;

-- name: ListConfigSpaceRows :many
SELECT id, config_id, author, created_at, space_id, global_seq
FROM config_spaces
ORDER BY config_id, id ASC;

-- name: RenameConfigRow :exec
UPDATE configs SET name = ? WHERE id = ?;

-- name: SoftDeleteConfigRow :exec
UPDATE configs SET deleted_at = ? WHERE id = ?;

-- name: ListConfigVersionRows :many
SELECT id, config_id, version, value, created_at, author, global_seq
FROM config_versions
ORDER BY config_id, version;

-- name: ListConfigVersionsByConfigID :many
SELECT id, config_id, version, value, created_at, author, global_seq
FROM config_versions WHERE config_id = ?
ORDER BY version ASC;

-- name: GetNextConfigVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM config_versions
WHERE config_id = ?;

-- name: InsertConfigVersion :one
INSERT INTO config_versions (config_id, version, value, created_at, author, global_seq)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, config_id, version, value, created_at, author, global_seq;

-- name: CountValueDirectorySiblingsWithName :one
SELECT COUNT(*) FROM value_directories
WHERE space_id = ? AND parent_id = ? AND name = ? AND id != ?;

-- name: GetValueDirectoryByID :one
SELECT id, space_id, name, parent_id, created_at, author
FROM value_directories
WHERE id = ?;

-- name: ListValueDirectories :many
SELECT id, space_id, name, parent_id, created_at, author
FROM value_directories
ORDER BY space_id, parent_id, name;

-- name: SetValueDirectoryName :exec
UPDATE value_directories SET name = ? WHERE id = ?;

-- name: InsertValueDirectory :one
INSERT INTO value_directories (space_id, name, parent_id, created_at, author)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, name, parent_id, created_at, author;

-- name: SetValueDirectoryParent :exec
UPDATE value_directories SET parent_id = ? WHERE id = ?;

-- name: DeleteValueDirectory :exec
DELETE FROM value_directories WHERE id = ?;

-- name: CountChildValueDirectories :one
SELECT COUNT(*) FROM value_directories WHERE parent_id = ?;

-- name: CountSecretsInDirectory :one
SELECT COUNT(*) FROM secrets WHERE value_directory_id = ? AND deleted_at = 0;

-- name: CountConfigsInDirectory :one
SELECT COUNT(*) FROM configs WHERE value_directory_id = ? AND deleted_at = 0;

-- name: SetSecretValueDirectoryID :exec
UPDATE secrets SET value_directory_id = ? WHERE id = ?;

-- name: SetConfigValueDirectoryID :exec
UPDATE configs SET value_directory_id = ? WHERE id = ?;

-- Container reads are hand-written in assets.go: the current space is the
-- newest asset_spaces row and reads exclude soft-deleted assets.

-- name: CountDirectorySiblingsWithKey :one
SELECT COUNT(*) FROM asset_directories
WHERE space_id = ? AND parent_id = ? AND key = ? AND id != ?;

-- name: InsertAssetRow :one
INSERT INTO assets (key, asset_directory_id, created_at)
VALUES (?, ?, ?)
RETURNING id;

-- name: InsertAssetSpaceRow :exec
INSERT INTO asset_spaces (asset_id, author, created_at, space_id, global_seq)
VALUES (?, ?, ?, ?, ?);

-- name: ListAssetSpaceRowsByAssetID :many
SELECT id, asset_id, author, created_at, space_id, global_seq
FROM asset_spaces WHERE asset_id = ?
ORDER BY id ASC;

-- name: ListAssetSpaceRows :many
SELECT id, asset_id, author, created_at, space_id, global_seq
FROM asset_spaces
ORDER BY asset_id, id ASC;

-- name: RenameAssetKey :exec
UPDATE assets SET key = ? WHERE id = ?;

-- name: SetAssetDirectoryID :exec
UPDATE assets SET asset_directory_id = ? WHERE id = ?;

-- name: SoftDeleteAssetRow :exec
UPDATE assets SET deleted_at = ? WHERE id = ?;

-- name: GetAssetDirectoryByID :one
SELECT id, space_id, key, parent_id, created_at, author
FROM asset_directories
WHERE id = ?;

-- name: ListAssetDirectories :many
SELECT id, space_id, key, parent_id, created_at, author
FROM asset_directories
ORDER BY space_id, parent_id, key;

-- name: SetAssetDirectoryKey :exec
UPDATE asset_directories SET key = ? WHERE id = ?;

-- name: InsertAssetDirectory :one
INSERT INTO asset_directories (space_id, key, parent_id, created_at, author)
VALUES (?, ?, ?, ?, ?)
RETURNING id, space_id, key, parent_id, created_at, author;

-- name: SetAssetDirectoryParent :exec
UPDATE asset_directories SET parent_id = ? WHERE id = ?;

-- name: DeleteAssetDirectory :exec
DELETE FROM asset_directories WHERE id = ?;

-- name: CountAssetsInDirectory :one
SELECT COUNT(*) FROM assets WHERE asset_directory_id = ? AND deleted_at = 0;

-- name: CountChildAssetDirectories :one
SELECT COUNT(*) FROM asset_directories WHERE parent_id = ?;

-- name: GetNextAssetVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM asset_versions
WHERE asset_id = ?;

-- name: InsertAssetVersion :one
INSERT INTO asset_versions (asset_id, version, created_at, author, size_bytes, sha256, global_seq)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, asset_id, version, created_at, author, size_bytes, sha256, global_seq;

-- name: CountAssetVersionsBySha :one
SELECT COUNT(*) FROM asset_versions WHERE sha256 = ?;

-- name: ListAssetIDsBySha :many
SELECT DISTINCT asset_id FROM asset_versions WHERE sha256 = ?;

-- name: InsertAssetStoreRow :one
INSERT INTO asset_store (id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at;

-- name: GetAssetStoreRowByID :one
SELECT id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at
FROM asset_store
WHERE id = ?;

-- name: GetAssetStoreRowBySha :one
SELECT id, sha256, size_bytes, inline_blob, local_status, remote_status, created_at
FROM asset_store
WHERE sha256 = ? AND sha256 != '';

-- name: ListAssetStoreRowMetas :many
SELECT id, sha256, size_bytes, CAST(LENGTH(inline_blob) AS INTEGER) AS inline_size, local_status, remote_status, created_at
FROM asset_store;

-- name: ListUnreferencedAssetStoreRows :many
SELECT s.id, s.sha256, s.size_bytes, CAST(LENGTH(s.inline_blob) AS INTEGER) AS inline_size, s.local_status, s.remote_status, s.created_at
FROM asset_store s
WHERE s.created_at < ? AND NOT EXISTS (SELECT 1 FROM asset_versions v WHERE v.sha256 = s.sha256);

-- name: CompleteAssetStoreRow :exec
UPDATE asset_store SET sha256 = ?, local_status = ?, remote_status = ? WHERE id = ?;

-- name: SetAssetStoreLocalStatus :exec
UPDATE asset_store SET local_status = ? WHERE id = ?;

-- name: SetAssetStoreRemoteStatus :exec
UPDATE asset_store SET remote_status = ? WHERE id = ?;

-- name: DeleteAssetStoreRow :exec
DELETE FROM asset_store WHERE id = ?;

-- name: GetUnfinishedAssetMigration :one
SELECT id, old_config_version_id, new_config_version_id, status, last_error,
       created_at, started_at, last_attempt_at, finished_at
FROM asset_migrations
WHERE status != 'finished'
ORDER BY id
LIMIT 1;

-- name: InsertAssetMigration :one
INSERT INTO asset_migrations (
    old_config_version_id, new_config_version_id, status, created_at
) VALUES (?, ?, 'pending', ?)
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: StartAssetMigration :one
UPDATE asset_migrations
SET status = 'running', started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END,
    last_attempt_at = ?, last_error = ''
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: RecordAssetMigrationError :one
UPDATE asset_migrations
SET status = 'running', last_attempt_at = ?, last_error = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: FinishAssetMigration :one
UPDATE asset_migrations
SET status = 'finished', last_error = '', finished_at = ?
WHERE id = ?
RETURNING id, old_config_version_id, new_config_version_id, status, last_error,
          created_at, started_at, last_attempt_at, finished_at;

-- name: ListSecretKeyslots :many
SELECT slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at
FROM secret_keyslots ORDER BY slot;

-- name: UpsertSecretKeyslot :exec
INSERT INTO secret_keyslots (slot, smk_version, wrapped_smk, nonce, kdf_salt, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(slot) DO UPDATE SET
    smk_version = excluded.smk_version,
    wrapped_smk = excluded.wrapped_smk,
    nonce = excluded.nonce,
    kdf_salt = excluded.kdf_salt,
    created_at = excluded.created_at;

-- Container reads and the version-record list are hand-written in values.go:
-- the current space is the newest secret_spaces row and reads exclude
-- soft-deleted secrets.

-- name: InsertSecretRow :one
INSERT INTO secrets (name, value_directory_id, created_at)
VALUES (?, ?, ?)
RETURNING id;

-- name: InsertSecretSpaceRow :exec
INSERT INTO secret_spaces (secret_id, author, created_at, space_id, global_seq)
VALUES (?, ?, ?, ?, ?);

-- name: ListSecretSpaceRowsBySecretID :many
SELECT id, secret_id, author, created_at, space_id, global_seq
FROM secret_spaces WHERE secret_id = ?
ORDER BY id ASC;

-- name: ListSecretSpaceRows :many
SELECT id, secret_id, author, created_at, space_id, global_seq
FROM secret_spaces
ORDER BY secret_id, id ASC;

-- name: RenameSecretRow :exec
UPDATE secrets SET name = ? WHERE id = ?;

-- name: SoftDeleteSecretRow :exec
UPDATE secrets SET deleted_at = ? WHERE id = ?;

-- name: ListSecretVersionMetas :many
SELECT id, secret_id, version, created_at, author, global_seq
FROM secret_versions
ORDER BY secret_id, version;

-- name: GetNextSecretVersionNumber :one
SELECT COALESCE(MAX(version), 0) + 1
FROM secret_versions
WHERE secret_id = ?;

-- name: InsertSecretVersion :one
INSERT INTO secret_versions (secret_id, version, smk_version, ciphertext, nonce, created_at, author, global_seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, secret_id, version, smk_version, ciphertext, nonce, created_at, author, global_seq;

-- name: ListSecretVersionIDsBySecretID :many
SELECT id FROM secret_versions WHERE secret_id = ? ORDER BY version;

-- name: ListSecretVersionsBySecretID :many
SELECT id, secret_id, version, created_at, author, global_seq
FROM secret_versions WHERE secret_id = ?
ORDER BY version ASC;

-- name: ListConfigVersionIDsByConfigID :many
SELECT id FROM config_versions WHERE config_id = ? ORDER BY version;

-- name: GetSystemSecret :one
SELECT name, smk_version, ciphertext, nonce, created_at, updated_at
FROM system_secrets
WHERE name = ?;

-- name: UpsertSystemSecret :exec
INSERT INTO system_secrets (name, smk_version, ciphertext, nonce, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    smk_version = excluded.smk_version,
    ciphertext = excluded.ciphertext,
    nonce = excluded.nonce,
    updated_at = excluded.updated_at;


-- name: GetLatestConfig :one
select * from system_config_revisions order by id desc limit 1;

-- name: GetConfigByID :one
SELECT id, updated_at, config_blob FROM system_config_revisions WHERE id = ?;

-- The template list read is hand-written in authz.go: a template row is its
-- identity joined with the version log for created_at/author (v1 row) and
-- current content (latest row). Creation appends the v1 version row in the
-- same tx; content updates are pure appends.

-- name: CreateAuthzRuleTemplate :one
INSERT INTO authz_rule_templates (name, builtin, deleted_at)
VALUES (?, 0, 0) RETURNING id;

-- name: AppendAuthzRuleTemplateVersion :exec
INSERT INTO authz_rule_template_versions (template_id, version, created_at, author, data_blob, global_seq)
SELECT @template_id, COALESCE(MAX(version), 0) + 1, @created_at, @author, @data_blob, @global_seq
FROM authz_rule_template_versions
WHERE template_id = @template_id;

-- name: UpdateAuthzRuleTemplateName :exec
UPDATE authz_rule_templates SET name = ? WHERE id = ?;

-- name: SetAuthzRuleTemplateDeletedAt :exec
UPDATE authz_rule_templates SET deleted_at = ? WHERE id = ?;

-- name: UpsertAuthzRuleTemplateIdentity :exec
INSERT INTO authz_rule_templates (id, name, builtin, deleted_at)
VALUES (?, ?, 1, 0)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    builtin = 1,
    deleted_at = 0;

-- name: GetLatestAuthzRuleTemplateVersionBlob :one
SELECT data_blob FROM authz_rule_template_versions
WHERE template_id = ? ORDER BY id DESC LIMIT 1;

-- name: ListAuthzGrantRows :many
SELECT id, user_id, template_id, author, created_at, data_blob FROM authz_grants
WHERE deleted_at = 0;

-- name: InsertAuthzGrantRow :one
INSERT INTO authz_grants (user_id, template_id, author, created_at, data_blob)
VALUES (?, ?, ?, ?, ?) RETURNING id;

-- name: SetAuthzGrantDeletedAt :exec
UPDATE authz_grants SET deleted_at = ? WHERE id = ?;

-- name: ListGlobalAccessRuleRows :many
SELECT id, name, author, created_at, data_blob FROM global_access_rules
WHERE deleted_at = 0;

-- name: InsertGlobalAccessRuleRow :one
INSERT INTO global_access_rules (name, author, created_at, data_blob)
VALUES (?, ?, ?, ?) RETURNING id;

-- name: SetGlobalAccessRuleDeletedAt :exec
UPDATE global_access_rules SET deleted_at = ? WHERE id = ?;

-- name: CountGlobalAccessRuleRowsByName :one
SELECT COUNT(*) FROM global_access_rules WHERE name = ?;

-- Network policy content lives in network_policy_versions; the list read is
-- hand-written in network_policies.go (identity joined with the latest
-- version row). Every content write advances the global sequence in the same
-- tx because policies are cluster network map render inputs.

-- name: CreateNetworkPolicyRow :one
INSERT INTO network_policies (deleted_at) VALUES (0) RETURNING id;

-- name: AppendNetworkPolicyVersion :exec
INSERT INTO network_policy_versions (policy_id, version, created_at, author, data_blob, global_seq)
SELECT @policy_id, COALESCE(MAX(version), 0) + 1, @created_at, @author, @data_blob, @global_seq
FROM network_policy_versions
WHERE policy_id = @policy_id;

-- name: SetNetworkPolicyDeletedAt :exec
UPDATE network_policies SET deleted_at = ? WHERE id = ?;
