package pq

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jptrs93/goutil/logu"
)

// One-time v0.0.614 shape migration: users, spaces, value_directories,
// asset_directories, system_config_revisions, nix_store_resets,
// agent_sessions, user_sessions, and secret_keyslots became the append-only
// *_event_log tables. A legacy table still under its old name is either
// already event-shaped (an unreleased v0.0.614 build: renamed in place before
// the schema runs, old indexes dropped) or the pre-event shape (left for the
// schema to create the new table beside it, then every row copied in as a
// seq-0 create event and the old table dropped). Remove after every active
// cluster has rolled forward, per the migrations.sql history-note convention.

type legacyEventTable struct {
	legacy string
	table  string
	// columns maps each new column to the expression that fills it from the
	// legacy row; a plain name is taken from the legacy row when it exists
	// and from fallback otherwise.
	columns  [][2]string
	fallback map[string]string
	where    string
	indexes  []string
}

var legacyEventTables = []legacyEventTable{
	{legacy: "users", table: "user_event_log", indexes: []string{"idx_users_user_id"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "created_at"}, {"author", "0"}, {"user_id", "id"}, {"event_type", "1"}, {"name", "name"}, {"data_blob", "data_blob"}, {"created_at", "created_at"}}},
	{legacy: "spaces", table: "space_event_log", indexes: []string{"idx_spaces_space_id"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "0"}, {"author", "0"}, {"space_id", "id"}, {"event_type", "1"}, {"name", "name"}},
		where:   "WHERE id NOT IN (SELECT space_id FROM space_event_log)"},
	{legacy: "value_directories", table: "value_directory_event_log", indexes: []string{"idx_value_directories_directory_id"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "created_at"}, {"author", "author"}, {"directory_id", "id"}, {"event_type", "1"}, {"space_id", "space_id"}, {"name", "name"}, {"parent_id", "parent_id"}, {"created_at", "created_at"}}},
	{legacy: "asset_directories", table: "asset_directory_event_log", indexes: []string{"idx_asset_directories_directory_id"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "created_at"}, {"author", "author"}, {"directory_id", "id"}, {"event_type", "1"}, {"space_id", "space_id"}, {"key", "key"}, {"parent_id", "parent_id"}, {"created_at", "created_at"}}},
	{legacy: "system_config_revisions", table: "system_config_event_log",
		columns: [][2]string{{"id", "id"}, {"global_seq", "0"}, {"event_time", "updated_at"}, {"author", "0"}, {"event_type", "CASE WHEN id = (SELECT MIN(id) FROM system_config_revisions) THEN 1 ELSE 2 END"}, {"config_blob", "config_blob"}}},
	{legacy: "nix_store_resets", table: "nix_store_reset_event_log", indexes: []string{"idx_nix_store_resets_repo"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "requested_at"}, {"author", "0"}, {"repo", "repo"}, {"event_type", "1"}, {"requested_at", "requested_at"}}},
	{legacy: "agent_sessions", table: "agent_session_event_log", indexes: []string{"idx_agent_sessions_session_id", "idx_agent_sessions_user_id"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "created_at * 1000"}, {"author", "0"}, {"session_id", "id"}, {"event_type", "1"}, {"user_id", "user_id"}, {"created_at", "created_at"}, {"expires_at", "expires_at"}, {"token_hash", "token_hash"}, {"token_prefix", "token_prefix"}, {"revoked_at", "revoked_at"}, {"status", "status"}, {"requesting_address", "requesting_address"}, {"approval_code", "approval_code"}, {"approved_at", "approved_at"}}},
	{legacy: "user_sessions", table: "user_session_event_log", indexes: []string{"idx_user_sessions_session_id", "idx_user_sessions_user_id"},
		columns:  [][2]string{{"global_seq", "0"}, {"event_time", "created_at * 1000"}, {"author", "0"}, {"session_id", "id"}, {"event_type", "1"}, {"user_id", "user_id"}, {"created_at", "created_at"}, {"expires_at", "expires_at"}, {"token_hash", "token_hash"}, {"revoked_at", "revoked_at"}, {"kind", "kind"}, {"requesting_address", "requesting_address"}, {"user_agent", "user_agent"}},
		fallback: map[string]string{"kind": "0"}},
	{legacy: "secret_keyslots", table: "secret_keyslot_event_log", indexes: []string{"idx_secret_keyslots_key"},
		columns: [][2]string{{"global_seq", "0"}, {"event_time", "created_at"}, {"author", "0"},
			{"kind", "CASE WHEN slot = 'machine' THEN 1 ELSE 2 END"},
			{"node_id", "CASE WHEN slot = 'machine' THEN " + primaryNodeIDExpr + " ELSE 0 END"},
			{"event_type", "1"}, {"smk_version", "smk_version"}, {"wrapped_smk", "wrapped_smk"}, {"nonce", "nonce"}, {"kdf_salt", "kdf_salt"}}},
}

const primaryNodeIDExpr = `COALESCE((SELECT n.node_id FROM node_event_log n
	WHERE n.version = (SELECT MAX(version) FROM node_event_log WHERE node_id = n.node_id)
	AND n.status IN (4, 5, 6, 7)
	AND EXISTS (SELECT 1 FROM json_each(n.roles) WHERE value = 0)
	LIMIT 1), 0)`

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int64
		var name, typ string
		var notNull, pk int64
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// renameLegacyEventTables runs before the schema. An event-shaped table
// still under its old name is renamed to its *_event_log name; a pre-event
// table is left in place and reported with its column set for the copy.
func renameLegacyEventTables(db *sql.DB) map[string]map[string]bool {
	ctx := logu.AddTag(context.Background(), "Store")
	pending := map[string]map[string]bool{}
	for _, spec := range legacyEventTables {
		cols, err := tableColumns(ctx, db, spec.legacy)
		if err != nil {
			panic(fmt.Errorf("inspecting %s: %w", spec.legacy, err))
		}
		if len(cols) == 0 {
			continue
		}
		if !cols["event_type"] {
			pending[spec.legacy] = cols
			continue
		}
		for _, index := range spec.indexes {
			if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
				panic(fmt.Errorf("dropping index %s: %w", index, err))
			}
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE `+spec.legacy+` RENAME TO `+spec.table); err != nil {
			panic(fmt.Errorf("renaming %s to %s: %w", spec.legacy, spec.table, err))
		}
		slog.InfoContext(ctx, "renamed event table", "from", spec.legacy, "to", spec.table)
	}
	return pending
}

// copyLegacyEventTables copies each pre-event table's rows into its new
// event table as seq-0 create events and drops the old table.
func copyLegacyEventTables(db *sql.DB, pending map[string]map[string]bool) {
	ctx := logu.AddTag(context.Background(), "Store")
	for _, spec := range legacyEventTables {
		cols, ok := pending[spec.legacy]
		if !ok {
			continue
		}
		var targets, sources []string
		for _, c := range spec.columns {
			target, source := c[0], c[1]
			if isIdentifier(source) && !cols[source] {
				fallback, ok := spec.fallback[target]
				if !ok {
					panic(fmt.Errorf("legacy %s has no column %s and no fallback", spec.legacy, source))
				}
				source = fallback
			}
			targets = append(targets, target)
			sources = append(sources, source)
		}
		stmt := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s %s ORDER BY %s`,
			spec.table, strings.Join(targets, ", "), strings.Join(sources, ", "), spec.legacy, spec.where, orderColumn(cols))
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			panic(err)
		}
		result, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			tx.Rollback()
			panic(fmt.Errorf("copying legacy %s: %w", spec.legacy, err))
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+spec.legacy); err != nil {
			tx.Rollback()
			panic(fmt.Errorf("dropping legacy %s: %w", spec.legacy, err))
		}
		if err := tx.Commit(); err != nil {
			panic(err)
		}
		copied, _ := result.RowsAffected()
		slog.InfoContext(ctx, "rebuilt table as an append-only event table", "from", spec.legacy, "to", spec.table, "rows", copied)
	}
}

func isIdentifier(expr string) bool {
	for _, r := range expr {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return expr != "" && (expr[0] < '0' || expr[0] > '9')
}

func orderColumn(cols map[string]bool) string {
	if cols["id"] {
		return "id"
	}
	return "rowid"
}
