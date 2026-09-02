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

-- Reads are hand-written in scheduled_instances.go: an instance's current
-- shape is its highest-version event row. Every event carries the full
-- identity (immutable per instance, supplied by the caller from the resolved
-- instance) plus the new state. The version-1 write is the creation.

-- name: NextScheduledInstanceID :one
SELECT COALESCE(MAX(scheduled_instance_id), 0) + 1 FROM scheduled_instance_event_log;

-- name: AppendScheduledInstanceEvent :exec
INSERT INTO scheduled_instance_event_log (
    scheduled_instance_id, version, global_seq, event_time, created_time,
    deployment_id, deployment_version, deployment_spec_version,
    node_id, instance_ordinal, space_id, state)
SELECT @scheduled_instance_id, COALESCE(MAX(version), 0) + 1, @global_seq, @event_time,
       COALESCE(MIN(created_time), @event_time),
       @deployment_id, @deployment_version, @deployment_spec_version,
       @node_id, @instance_ordinal, @space_id, @state
FROM scheduled_instance_event_log
WHERE scheduled_instance_id = @scheduled_instance_id;

-- name: GetScheduledInstance :one
SELECT id, global_seq, event_time, created_time, scheduled_instance_id, version,
       deployment_id, deployment_version, deployment_spec_version,
       node_id, instance_ordinal, space_id, state
FROM scheduled_instance_event_log
WHERE scheduled_instance_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListNonFinalScheduledInstances :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.scheduled_instance_id, e.version,
       e.deployment_id, e.deployment_version, e.deployment_spec_version,
       e.node_id, e.instance_ordinal, e.space_id, e.state
FROM scheduled_instance_event_log e
JOIN (SELECT scheduled_instance_id, MAX(version) AS version
      FROM scheduled_instance_event_log
      GROUP BY scheduled_instance_id) latest
  ON latest.scheduled_instance_id = e.scheduled_instance_id AND latest.version = e.version
WHERE e.state != 2
ORDER BY e.scheduled_instance_id ASC;

-- name: ListLatestScheduledInstancePerOrdinal :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.scheduled_instance_id, e.version,
       e.deployment_id, e.deployment_version, e.deployment_spec_version,
       e.node_id, e.instance_ordinal, e.space_id, e.state
FROM scheduled_instance_event_log e
JOIN (SELECT scheduled_instance_id, MAX(version) AS version
      FROM scheduled_instance_event_log
      GROUP BY scheduled_instance_id) latest
  ON latest.scheduled_instance_id = e.scheduled_instance_id AND latest.version = e.version
JOIN (SELECT deployment_id, instance_ordinal, MAX(scheduled_instance_id) AS scheduled_instance_id
      FROM scheduled_instance_event_log
      GROUP BY deployment_id, instance_ordinal) newest
  ON newest.scheduled_instance_id = e.scheduled_instance_id
ORDER BY e.scheduled_instance_id ASC;

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

-- Config and secret reads and writes are hand-written in values.go: the
-- current state is the highest-version event row, event_type is the deletion
-- truth, and a row with a non-NULL value payload is a pinnable value version.

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

-- Asset reads and writes are hand-written in assets.go: the current state is
-- the highest-version event row, event_type is the deletion truth, and a row
-- with a non-NULL content payload is a pinnable content version.

-- name: CountDirectorySiblingsWithKey :one
SELECT COUNT(*) FROM asset_directories
WHERE space_id = ? AND parent_id = ? AND key = ? AND id != ?;

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

-- name: CountChildAssetDirectories :one
SELECT COUNT(*) FROM asset_directories WHERE parent_id = ?;

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
WHERE s.created_at < ? AND NOT EXISTS (SELECT 1 FROM asset_event_log v WHERE v.sha256 = s.sha256);

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

-- name: ListNodeEvents :many
SELECT id, global_seq, event_time, created_time, author, node_id, version,
       name, identifier, enrolled_time, status, roles, addresses,
       wg_public_key, allowed_spaces, event_type
FROM node_event_log
WHERE node_id = ?
ORDER BY version;

-- name: NextSecretID :one
SELECT COALESCE(MAX(secret_id), 0) + 1 FROM secret_event_log;

-- name: ListSecretVersionIDsBySecretID :many
SELECT id FROM secret_event_log
WHERE secret_id = ? AND ciphertext IS NOT NULL
ORDER BY value_version;

-- name: NextConfigID :one
SELECT COALESCE(MAX(config_id), 0) + 1 FROM config_event_log;

-- name: GetLatestConfigEvent :one
SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id,
       value, event_type
FROM config_event_log
WHERE config_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListConfigEvents :many
SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id,
       value, event_type
FROM config_event_log
WHERE config_id = ?
ORDER BY version;

-- name: ListAllConfigEvents :many
SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id,
       value, event_type
FROM config_event_log
ORDER BY config_id, version;

-- name: ListConfigVersionIDsByConfigID :many
SELECT id FROM config_event_log
WHERE config_id = ? AND value IS NOT NULL
ORDER BY value_version;

-- name: NextAssetID :one
SELECT COALESCE(MAX(asset_id), 0) + 1 FROM asset_event_log;

-- name: GetLatestAssetEvent :one
SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id,
       size_bytes, sha256, event_type
FROM asset_event_log
WHERE asset_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListAssetEvents :many
SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id,
       size_bytes, sha256, event_type
FROM asset_event_log
WHERE asset_id = ?
ORDER BY version;

-- name: ListAllAssetEvents :many
SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id,
       size_bytes, sha256, event_type
FROM asset_event_log
ORDER BY asset_id, version;

-- name: NextNetworkPolicyID :one
SELECT COALESCE(MAX(policy_id), 0) + 1 FROM network_policy_event_log;

-- name: GetLatestNetworkPolicyEvent :one
SELECT id, global_seq, event_time, created_time, author, policy_id, version,
       data_blob, event_type
FROM network_policy_event_log
WHERE policy_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListLatestNetworkPolicyEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.policy_id,
       e.version, e.data_blob, e.event_type
FROM network_policy_event_log e
JOIN (SELECT policy_id, MAX(version) AS version
      FROM network_policy_event_log GROUP BY policy_id) latest
  ON latest.policy_id = e.policy_id AND latest.version = e.version
ORDER BY e.policy_id;

-- name: NextAuthzRuleTemplateID :one
SELECT COALESCE(MAX(template_id), 0) + 1 FROM authz_rule_template_event_log;

-- name: GetLatestAuthzRuleTemplateEvent :one
SELECT id, global_seq, event_time, created_time, author, template_id, version,
       name, builtin, data_blob, event_type
FROM authz_rule_template_event_log
WHERE template_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListLatestAuthzRuleTemplateEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.template_id,
       e.version, e.name, e.builtin, e.data_blob, e.event_type
FROM authz_rule_template_event_log e
JOIN (SELECT template_id, MAX(version) AS version
      FROM authz_rule_template_event_log GROUP BY template_id) latest
  ON latest.template_id = e.template_id AND latest.version = e.version
ORDER BY e.template_id;

-- name: NextAuthzGrantID :one
SELECT COALESCE(MAX(grant_id), 0) + 1 FROM authz_grant_event_log;

-- name: GetLatestAuthzGrantEvent :one
SELECT id, global_seq, event_time, created_time, author, grant_id, version,
       user_id, template_id, data_blob, event_type
FROM authz_grant_event_log
WHERE grant_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListLatestAuthzGrantEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.grant_id,
       e.version, e.user_id, e.template_id, e.data_blob, e.event_type
FROM authz_grant_event_log e
JOIN (SELECT grant_id, MAX(version) AS version
      FROM authz_grant_event_log GROUP BY grant_id) latest
  ON latest.grant_id = e.grant_id AND latest.version = e.version
ORDER BY e.grant_id;

-- name: NextGlobalAccessRuleID :one
SELECT COALESCE(MAX(rule_id), 0) + 1 FROM global_access_rule_event_log;

-- name: GetLatestGlobalAccessRuleEvent :one
SELECT id, global_seq, event_time, created_time, author, rule_id, version,
       name, disabled, data_blob, event_type
FROM global_access_rule_event_log
WHERE rule_id = ?
ORDER BY version DESC LIMIT 1;

-- name: ListLatestGlobalAccessRuleEvents :many
SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.rule_id,
       e.version, e.name, e.disabled, e.data_blob, e.event_type
FROM global_access_rule_event_log e
JOIN (SELECT rule_id, MAX(version) AS version
      FROM global_access_rule_event_log GROUP BY rule_id) latest
  ON latest.rule_id = e.rule_id AND latest.version = e.version
ORDER BY e.rule_id;

-- name: CountGlobalAccessRuleEventsByName :one
SELECT COUNT(*) FROM global_access_rule_event_log WHERE name = ?;
