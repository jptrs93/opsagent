package pq

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

var legacyEventTables = []string{
	"secret_event_log", "config_event_log", "asset_event_log", "value_directory_event_log", "asset_directory_event_log",
	"space_event_log", "user_event_log", "network_policy_event_log", "authz_rule_template_event_log", "authz_grant_event_log", "global_access_rule_event_log",
	"agent_session_event_log", "user_session_event_log", "nix_store_reset_event_log", "secret_keyslot_event_log", "system_config_event_log",
	"deployment_event_log", "scheduled_instance_event_log", "scheduled_instance_status_log", "node_event_log", "node_status_log",
}

// backupLegacyDatabase copies the database file next to itself before the
// one-time materialisation reshapes it: the rename below and the schema that
// follows run outside the migration's transaction, so a start refused by the
// verification leaves a file that neither v0.0.614 nor the old tables' shape
// fits. An existing copy is kept because a retry runs on the renamed file
// and would overwrite the only copy of the original.
func backupLegacyDatabase(db *sql.DB, dbPath string) string {
	ctx := logu.AddTag(context.Background(), "Store")
	q := &Queries{db: &conn{DBTX: db, root: db}}
	present, err := q.existingTables(ctx, legacyEventTables)
	if err != nil {
		panic(fmt.Errorf("backing up before materialising: %w", err))
	}
	if len(present) == 0 {
		return ""
	}
	backup := dbPath + ".pre-materialise"
	if _, err := os.Stat(backup); err == nil {
		slog.InfoContext(ctx, fmt.Sprintf("keeping the existing copy of the database from before the move at %s", backup))
		return backup
	} else if !errors.Is(err, fs.ErrNotExist) {
		panic(fmt.Errorf("backing up before materialising: %w", err))
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		panic(fmt.Errorf("backing up before materialising: %w", err))
	}
	slog.InfoContext(ctx, fmt.Sprintf("copied the database to %s before materialising the tables", backup))
	return backup
}

// renameLegacyStatusLog moves the old scheduled_instance_status history table
// out of the way of the materialised table of the same name before the
// schema is applied: CREATE TABLE IF NOT EXISTS would otherwise keep the old
// shape silently. The old table is recognised by its global_seq column.
func renameLegacyStatusLog(db *sql.DB) {
	var legacy int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduled_instance_status') WHERE name = 'global_seq'`).Scan(&legacy); err != nil {
		panic(fmt.Errorf("inspecting scheduled_instance_status: %w", err))
	}
	if legacy == 0 {
		return
	}
	for _, stmt := range []string{
		`ALTER TABLE scheduled_instance_status RENAME TO scheduled_instance_status_log`,
		`DROP INDEX IF EXISTS idx_scheduled_instance_status_deployment`,
		`DROP INDEX IF EXISTS idx_scheduled_instance_status_seq`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			panic(fmt.Errorf("renaming the legacy scheduled_instance_status table: %w", err))
		}
	}
}

// materialiseLegacyTables is the one-time move from the per-entity event
// tables of the reduced types to their materialised tables. It runs when any
// of the old tables is still present: it rebuilds every materialised table
// from the log, checks the rebuilt rows against the old tables' live rows,
// and drops the old tables, all in one transaction. A mismatch leaves the
// tables untouched and refuses to start, naming the copy of the file taken
// before the move. Remove after every active cluster has rolled forward, per
// the migrations.sql convention.
func materialiseLegacyTables(db *sql.DB, backup string) {
	ctx := logu.AddTag(context.Background(), "Store")
	q := &Queries{db: &conn{DBTX: db, root: db}}
	present, err := q.existingTables(ctx, legacyEventTables)
	if err != nil {
		panic(fmt.Errorf("materialising the legacy tables: %w", err))
	}
	if len(present) == 0 {
		return
	}
	err = q.Tx(ctx, func(tx *Queries) error {
		if err := tx.RebuildFromLog(ctx); err != nil {
			return err
		}
		if err := tx.verifyMaterialisedAgainstLegacy(ctx, present); err != nil {
			return err
		}
		for _, table := range present {
			if _, err := tx.db.ExecContext(ctx, `DROP TABLE `+table); err != nil {
				return err
			}
		}
		slog.InfoContext(ctx, fmt.Sprintf("materialised the tables from the write log and dropped %v, the copy at %s can be deleted once the cluster is not going back to v0.0.614", present, backup))
		return nil
	})
	if err != nil {
		panic(fmt.Errorf("materialising the legacy tables: %w, the database as it was before the move is kept at %s", err, backup))
	}
}

func (q *Queries) existingTables(ctx context.Context, names []string) ([]string, error) {
	var out []string
	for _, name := range names {
		var n int64
		if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, name)
		}
	}
	return out, nil
}

type legacyCheck struct {
	legacy, rebuilt string
	// normalise re-encodes a legacy column so bytes written by an older
	// encoder, or JSON spelt differently, compare equal to the reducer's
	// output.
	normalise map[int]func(any) (any, error)
}

func reencode[T any](decode func([]byte) (*T, error), encode func(*T) []byte) func(any) (any, error) {
	return reencodeWith(decode, encode, func(*T) {})
}

func reencodeWith[T any](decode func([]byte) (*T, error), encode func(*T) []byte, adjust func(*T)) func(any) (any, error) {
	return func(v any) (any, error) {
		b, _ := v.([]byte)
		d, err := decode(b)
		if err != nil {
			return nil, err
		}
		adjust(d)
		return notNullBlob(encode(d)), nil
	}
}

func canonicalJSONList[T any](adjust func([]T) []T) func(any) (any, error) {
	return func(v any) (any, error) {
		text, _ := v.(string)
		var items []T
		if err := json.Unmarshal([]byte(text), &items); err != nil {
			return nil, err
		}
		if adjust != nil {
			items = adjust(items)
		}
		return jsonList(items), nil
	}
}

func stripVersionFacts(d *apigen.Deployment) {
	d.ID = 0
}

// verifyMaterialisedAgainstLegacy compares the facts of every live row the
// old tables hold with the rebuilt tables: identities with their placement,
// name, and newest value version; versions with their value; latest-only
// rows with their document. Creation times are not compared: the rebuilt
// tables take them from the entity's first logged write, which for a
// latest-only legacy table can postdate the value the old row carried.
func (q *Queries) verifyMaterialisedAgainstLegacy(ctx context.Context, present []string) error {
	latestLive := func(table, key string) string {
		return `WHERE id IN (SELECT MAX(id) FROM ` + table + ` GROUP BY ` + key + `) AND event_type != 3 ORDER BY ` + key
	}
	firstRow := func(table, key string) string {
		return `(SELECT MIN(p.id) FROM ` + table + ` p WHERE p.` + key + ` = ` + table + `.` + key + `)`
	}
	final := fmt.Sprint(int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
	liveDeployments := `(SELECT deployment_id FROM deployment_event_log WHERE id IN (SELECT MAX(id) FROM deployment_event_log GROUP BY deployment_id) AND event_type != 3)`
	latestInstances := `(SELECT * FROM scheduled_instance_event_log WHERE id IN (SELECT MAX(id) FROM scheduled_instance_event_log GROUP BY scheduled_instance_id))`
	checks := map[string][]legacyCheck{
		"deployment_event_log": {
			{legacy: `SELECT deployment_id, version, spec_version, value FROM deployment_event_log
WHERE id IN (SELECT MAX(id) FROM deployment_event_log GROUP BY deployment_id) AND event_type != 3 ORDER BY deployment_id`,
				rebuilt:   `SELECT d.id, d.version, d.spec_version, v.value FROM deployments d JOIN deployment_versions v ON v.deployment_id = d.id AND v.version = d.version ORDER BY d.id`,
				normalise: map[int]func(any) (any, error){3: reencodeWith(apigen.DecodeDeployment, (*apigen.Deployment).Encode, stripVersionFacts)}},
			{legacy: `SELECT deployment_id, version, spec_version, value FROM deployment_event_log
WHERE event_type != 3 AND (deployment_id, version) IN (SELECT deployment_id, version FROM deployment_versions) ORDER BY deployment_id, version`,
				rebuilt:   `SELECT deployment_id, version, spec_version, value FROM deployment_versions ORDER BY deployment_id, version`,
				normalise: map[int]func(any) (any, error){3: reencodeWith(apigen.DecodeDeployment, (*apigen.Deployment).Encode, stripVersionFacts)}},
		},
		"scheduled_instance_event_log": {{
			legacy: `SELECT l.scheduled_instance_id, l.deployment_id, l.deployment_version, l.deployment_spec_version, l.node_id, l.instance_ordinal, l.space_id, l.state
FROM ` + latestInstances + ` l WHERE l.state != ` + final + ` OR (l.deployment_id IN ` + liveDeployments + `
  AND NOT EXISTS (SELECT 1 FROM ` + latestInstances + ` o WHERE o.deployment_id = l.deployment_id AND o.instance_ordinal = l.instance_ordinal AND o.state != ` + final + `)
  AND l.scheduled_instance_id = (SELECT MAX(n.scheduled_instance_id) FROM ` + latestInstances + ` n WHERE n.deployment_id = l.deployment_id AND n.instance_ordinal = l.instance_ordinal))
ORDER BY l.scheduled_instance_id`,
			rebuilt: `SELECT id, deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state FROM scheduled_instances ORDER BY id`,
		}},
		"scheduled_instance_status_log": {{
			legacy: `SELECT s.scheduled_instance_id, s.updated_at, s.deployment_id, s.preparer_spec_version, s.preparer_artifact, s.preparer_inputs_status, s.preparer_image_status,
 s.runner_spec_version, s.runner_pid, s.runner_artifact, s.runner_status, s.runner_num_restarts, s.runner_last_restart_at, s.runner_extra_blob, s.runner_exit_code
FROM scheduled_instance_status_log s WHERE s.updated_at = (SELECT MAX(m.updated_at) FROM scheduled_instance_status_log m WHERE m.scheduled_instance_id = s.scheduled_instance_id)
  AND s.scheduled_instance_id IN (SELECT id FROM scheduled_instances) ORDER BY s.scheduled_instance_id`,
			rebuilt: `SELECT scheduled_instance_id, updated_at, deployment_id, preparer_spec_version, preparer_artifact, preparer_inputs_status, preparer_image_status,
 runner_spec_version, runner_pid, runner_artifact, runner_status, runner_num_restarts, runner_last_restart_at, runner_extra_blob, runner_exit_code
FROM scheduled_instance_status ORDER BY scheduled_instance_id`,
		}},
		"node_event_log": {{
			legacy: `SELECT node_id, name, identifier, status, roles, allowed_spaces, enrolled_time, enrollment_requested_at, COALESCE(json_extract(addresses, '$[0]'), ''), wg_public_key, host_addresses
FROM node_event_log WHERE id IN (SELECT MAX(id) FROM node_event_log GROUP BY node_id) AND event_type != 3 ORDER BY node_id`,
			rebuilt: `SELECT id, name, identifier, status, roles, allowed_spaces, enrolled_time, enrollment_requested_at, underlay_address, wg_public_key, host_addresses FROM nodes ORDER BY id`,
			normalise: map[int]func(any) (any, error){
				4: canonicalJSONList[int32](nil), 5: canonicalJSONList[int32](normaliseAllowedSpaces), 10: canonicalJSONList[string](nil),
			},
		}},
		"node_status_log": {{
			legacy: `SELECT s.node_id, s.updated_at, s.is_connected, s.last_connected_at, s.opendeploy_version, s.remote_address FROM node_status_log s
WHERE s.updated_at = (SELECT MAX(m.updated_at) FROM node_status_log m WHERE m.node_id = s.node_id) ORDER BY s.node_id`,
			rebuilt: `SELECT node_id, updated_at, is_connected, last_connected_at, opendeploy_version, remote_address FROM node_status ORDER BY node_id`,
		}},
		"agent_session_event_log": {{
			legacy: `SELECT ` + firstRow("agent_session_event_log", "session_id") + `, session_id, user_id, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at
FROM agent_session_event_log WHERE id IN (SELECT MAX(id) FROM agent_session_event_log GROUP BY session_id) ORDER BY 1`,
			rebuilt: `SELECT id, session_id, user_id, expires_at, token_hash, token_prefix, status, requesting_address, approval_code, approved_at FROM agent_sessions ORDER BY id`,
		}},
		"user_session_event_log": {{
			legacy: `SELECT ` + firstRow("user_session_event_log", "session_id") + `, session_id, user_id, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent
FROM user_session_event_log WHERE id IN (SELECT MAX(id) FROM user_session_event_log GROUP BY session_id) ORDER BY 1`,
			rebuilt: `SELECT id, session_id, user_id, expires_at, token_hash, revoked_at, kind, requesting_address, user_agent FROM user_sessions ORDER BY id`,
		}},
		"nix_store_reset_event_log": {{
			legacy: `SELECT ` + firstRow("nix_store_reset_event_log", "repo") + `, repo, requested_at
FROM nix_store_reset_event_log WHERE id IN (SELECT MAX(id) FROM nix_store_reset_event_log GROUP BY repo) ORDER BY 1`,
			rebuilt: `SELECT id, repo, requested_at FROM nix_store_resets ORDER BY id`,
		}},
		"secret_keyslot_event_log": {{
			legacy: `SELECT node_id * 256 + kind, kind, node_id, smk_version, wrapped_smk, nonce, NULLIF(kdf_salt, X''), event_time
FROM secret_keyslot_event_log WHERE id IN (SELECT MAX(id) FROM secret_keyslot_event_log GROUP BY kind, node_id) AND event_type != 3 ORDER BY 1`,
			rebuilt: `SELECT id, kind, node_id, smk_version, wrapped_smk, nonce, NULLIF(kdf_salt, X''), updated_at FROM secret_keyslots ORDER BY id`,
		}},
		"system_config_event_log": {{
			legacy:    `SELECT config_blob FROM system_config_event_log WHERE id = (SELECT MAX(id) FROM system_config_event_log)`,
			rebuilt:   `SELECT config_blob FROM system_config`,
			normalise: map[int]func(any) (any, error){0: reencode(apigen.DecodeSystemConfig, (*apigen.SystemConfig).Encode)},
		}},
		"space_event_log": {{
			legacy:  `SELECT space_id, name FROM space_event_log ` + latestLive("space_event_log", "space_id"),
			rebuilt: `SELECT id, name FROM spaces ORDER BY id`,
		}},
		"user_event_log": {{
			legacy:  `SELECT user_id, name, data_blob FROM user_event_log ` + latestLive("user_event_log", "user_id"),
			rebuilt: `SELECT id, name, data_blob FROM users ORDER BY id`,
		}},
		"network_policy_event_log": {{
			legacy:    `SELECT policy_id, data_blob FROM network_policy_event_log ` + latestLive("network_policy_event_log", "policy_id"),
			rebuilt:   `SELECT id, data_blob FROM network_policies ORDER BY id`,
			normalise: map[int]func(any) (any, error){1: reencode(apigen.DecodeNetworkPolicy, (*apigen.NetworkPolicy).Encode)},
		}},
		"authz_rule_template_event_log": {{
			legacy:    `SELECT template_id, name, builtin != 0, data_blob FROM authz_rule_template_event_log ` + latestLive("authz_rule_template_event_log", "template_id"),
			rebuilt:   `SELECT id, name, builtin != 0, data_blob FROM authz_rule_templates ORDER BY id`,
			normalise: map[int]func(any) (any, error){3: reencode(apigen.DecodeAuthzRuleTemplateSpec, (*apigen.AuthzRuleTemplateSpec).Encode)},
		}},
		"authz_grant_event_log": {{
			legacy:    `SELECT grant_id, user_id, template_id, data_blob FROM authz_grant_event_log ` + latestLive("authz_grant_event_log", "grant_id"),
			rebuilt:   `SELECT id, user_id, template_id, data_blob FROM authz_grants ORDER BY id`,
			normalise: map[int]func(any) (any, error){3: reencode(apigen.DecodeAuthzGrantSpec, (*apigen.AuthzGrantSpec).Encode)},
		}},
		"global_access_rule_event_log": {{
			legacy:    `SELECT rule_id, name, data_blob FROM global_access_rule_event_log ` + latestLive("global_access_rule_event_log", "rule_id"),
			rebuilt:   `SELECT id, name, data_blob FROM authz_global_rules ORDER BY id`,
			normalise: map[int]func(any) (any, error){2: reencode(apigen.DecodeAuthzGlobalRuleSpec, (*apigen.AuthzGlobalRuleSpec).Encode)},
		}},
		"secret_event_log": {
			{legacy: `SELECT e.secret_id, e.name, e.space_id, e.value_directory_id, e.value_version FROM secret_event_log e
JOIN (SELECT secret_id, MAX(version) AS version FROM secret_event_log GROUP BY secret_id) l ON l.secret_id = e.secret_id AND l.version = e.version
WHERE e.event_type != 3 ORDER BY e.secret_id`,
				rebuilt: `SELECT id, name, space_id, directory_id, value_version FROM secrets ORDER BY id`},
			{legacy: `SELECT v.secret_id, v.value_version, v.smk_version, v.ciphertext, v.nonce FROM secret_event_log v
WHERE v.value_changed != 0 AND v.secret_id IN (SELECT e.secret_id FROM secret_event_log e
  JOIN (SELECT secret_id, MAX(version) AS version FROM secret_event_log GROUP BY secret_id) l ON l.secret_id = e.secret_id AND l.version = e.version
  WHERE e.event_type != 3) ORDER BY v.secret_id, v.value_version`,
				rebuilt: `SELECT secret_id, value_version, smk_version, ciphertext, nonce FROM secret_versions ORDER BY secret_id, value_version`},
		},
		"config_event_log": {
			{legacy: `SELECT e.config_id, e.name, e.space_id, e.value_directory_id, e.value_version FROM config_event_log e
JOIN (SELECT config_id, MAX(version) AS version FROM config_event_log GROUP BY config_id) l ON l.config_id = e.config_id AND l.version = e.version
WHERE e.event_type != 3 ORDER BY e.config_id`,
				rebuilt: `SELECT id, name, space_id, directory_id, value_version FROM configs ORDER BY id`},
			{legacy: `SELECT v.config_id, v.value_version, v.value FROM config_event_log v
WHERE v.value_changed != 0 AND v.config_id IN (SELECT e.config_id FROM config_event_log e
  JOIN (SELECT config_id, MAX(version) AS version FROM config_event_log GROUP BY config_id) l ON l.config_id = e.config_id AND l.version = e.version
  WHERE e.event_type != 3) ORDER BY v.config_id, v.value_version`,
				rebuilt: `SELECT config_id, value_version, value FROM config_versions ORDER BY config_id, value_version`},
		},
		"asset_event_log": {
			{legacy: `SELECT e.asset_id, e.key, e.space_id, e.asset_directory_id, e.value_version FROM asset_event_log e
JOIN (SELECT asset_id, MAX(version) AS version FROM asset_event_log GROUP BY asset_id) l ON l.asset_id = e.asset_id AND l.version = e.version
WHERE e.event_type != 3 ORDER BY e.asset_id`,
				rebuilt: `SELECT id, key, space_id, directory_id, value_version FROM assets ORDER BY id`},
			{legacy: `SELECT v.asset_id, v.value_version, v.sha256, v.size_bytes, v.storage_key FROM asset_event_log v
WHERE v.value_changed != 0 AND v.asset_id IN (SELECT e.asset_id FROM asset_event_log e
  JOIN (SELECT asset_id, MAX(version) AS version FROM asset_event_log GROUP BY asset_id) l ON l.asset_id = e.asset_id AND l.version = e.version
  WHERE e.event_type != 3) ORDER BY v.asset_id, v.value_version`,
				rebuilt: `SELECT asset_id, value_version, sha256, size_bytes, storage_key FROM asset_versions ORDER BY asset_id, value_version`},
		},
		"value_directory_event_log": {
			{legacy: `SELECT directory_id, space_id, parent_id, name FROM value_directory_event_log
WHERE id IN (SELECT MAX(id) FROM value_directory_event_log GROUP BY directory_id) AND event_type != 3 ORDER BY directory_id`,
				rebuilt: `SELECT id, space_id, parent_id, name FROM value_directories ORDER BY id`},
		},
		"asset_directory_event_log": {
			{legacy: `SELECT directory_id, space_id, parent_id, key FROM asset_directory_event_log
WHERE id IN (SELECT MAX(id) FROM asset_directory_event_log GROUP BY directory_id) AND event_type != 3 ORDER BY directory_id`,
				rebuilt: `SELECT id, space_id, parent_id, key FROM asset_directories ORDER BY id`},
		},
	}
	for _, table := range present {
		for _, check := range checks[table] {
			legacy, err := q.rowFacts(ctx, check.legacy, check.normalise)
			if err != nil {
				return err
			}
			rebuilt, err := q.rowFacts(ctx, check.rebuilt, nil)
			if err != nil {
				return err
			}
			if len(legacy) != len(rebuilt) {
				return fmt.Errorf("%s: the write log rebuilds %d rows, the table holds %d", table, len(rebuilt), len(legacy))
			}
			for i := range legacy {
				if legacy[i] != rebuilt[i] {
					return fmt.Errorf("%s: the write log rebuilds %s, the table holds %s", table, rebuilt[i], legacy[i])
				}
			}
		}
	}
	return nil
}

func (q *Queries) rowFacts(ctx context.Context, query string, normalise map[int]func(any) (any, error)) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, f := range normalise {
			if values[i], err = f(values[i]); err != nil {
				return nil, err
			}
		}
		out = append(out, fmt.Sprintf("%v", values))
	}
	return out, rows.Err()
}
