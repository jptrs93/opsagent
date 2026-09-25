package pq

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
)

// One-time v0.0.613 shape migration: secret, config, and asset references
// moved from event log row ids to ValueRef{id, version} pairs. The old tags
// are reserved in the proto, so the generated decoders skip them; this walks
// the raw wire bytes instead. Remove after every active cluster has rolled
// forward, per the migrations.sql history-note convention.
func migrateValueRefs(db *sql.DB) {
	ctx := logu.AddTag(context.Background(), "Store")
	if err := migrateValueRefRows(ctx, db); err != nil {
		panic(fmt.Errorf("value reference migration: %w", err))
	}
}

type valueRefPair struct{ id, version uint64 }

type valueRefMaps struct {
	secrets, configs, assets map[uint64]valueRefPair
}

const unknownValueRef = "<unknown ref>"

type valueRefRewriter struct {
	maps       valueRefMaps
	table      string
	rowID      int64
	historical bool
	unknown    []string
}

type deploymentVersionKey struct{ deploymentID, version int64 }

func migrateValueRefRows(ctx context.Context, db *sql.DB) error {
	maps, err := loadValueRefMaps(ctx, db)
	if err != nil {
		return err
	}
	current, err := loadCurrentDeploymentVersions(ctx, db)
	if err != nil {
		return err
	}
	type pending struct {
		query string
		id    int64
		value []byte
	}
	var updates []pending
	for _, source := range []struct {
		table, column, identity string
		rewrite                 func(*valueRefRewriter, []byte) ([]byte, bool, error)
	}{
		{"deployment_event_log", "value", "deployment_id, version", (*valueRefRewriter).deployment},
		{"system_config_revisions", "config_blob", "0, 0", (*valueRefRewriter).systemConfig},
	} {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT id, %s, %s FROM %s ORDER BY id`, source.column, source.identity, source.table))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var value []byte
			var key deploymentVersionKey
			if err := rows.Scan(&id, &value, &key.deploymentID, &key.version); err != nil {
				rows.Close()
				return err
			}
			_, isCurrent := current[key]
			r := &valueRefRewriter{maps: maps, table: source.table, rowID: id, historical: key.deploymentID != 0 && !isCurrent}
			out, changed, err := source.rewrite(r, value)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%s row %d: %w", source.table, id, err)
			}
			for _, field := range r.unknown {
				slog.WarnContext(ctx, fmt.Sprintf("value reference migration: %s row %d version %d: %s replaced with %q", source.table, id, key.version, field, unknownValueRef), "dep", key.deploymentID)
			}
			if changed {
				updates = append(updates, pending{query: fmt.Sprintf(`UPDATE %s SET %s = ? WHERE id = ?`, source.table, source.column), id: id, value: out})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, u.query, u.value, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadCurrentDeploymentVersions(ctx context.Context, db *sql.DB) (map[deploymentVersionKey]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT deployment_id, MAX(version) FROM deployment_event_log GROUP BY deployment_id
UNION
SELECT e.deployment_id, e.deployment_version FROM scheduled_instance_event_log e
WHERE e.version = (SELECT MAX(version) FROM scheduled_instance_event_log WHERE scheduled_instance_id = e.scheduled_instance_id)
  AND e.state != 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[deploymentVersionKey]struct{}{}
	for rows.Next() {
		var key deploymentVersionKey
		if err := rows.Scan(&key.deploymentID, &key.version); err != nil {
			return nil, err
		}
		out[key] = struct{}{}
	}
	return out, rows.Err()
}

func loadValueRefMaps(ctx context.Context, db *sql.DB) (valueRefMaps, error) {
	load := func(table, idColumn string) (map[uint64]valueRefPair, error) {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT id, %s, value_version FROM %s WHERE value_changed != 0`, idColumn, table))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[uint64]valueRefPair{}
		for rows.Next() {
			var rowID, id, version int64
			if err := rows.Scan(&rowID, &id, &version); err != nil {
				return nil, err
			}
			out[uint64(rowID)] = valueRefPair{id: uint64(id), version: uint64(version)}
		}
		return out, rows.Err()
	}
	var maps valueRefMaps
	var err error
	if maps.secrets, err = load("secret_event_log", "secret_id"); err != nil {
		return maps, err
	}
	if maps.configs, err = load("config_event_log", "config_id"); err != nil {
		return maps, err
	}
	if maps.assets, err = load("asset_event_log", "asset_id"); err != nil {
		return maps, err
	}
	return maps, nil
}

type fieldRewrite func(num uint64, wireType uint64, payload []byte) (out []byte, changed bool, err error)

const (
	wireVarint = 0
	wireI64    = 1
	wireBytes  = 2
	wireI32    = 5
)

var errMalformed = errors.New("malformed protobuf")

func rewriteMessage(b []byte, fn fieldRewrite) ([]byte, bool, error) {
	out := make([]byte, 0, len(b))
	changedAny := false
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, false, errMalformed
		}
		start := b
		b = b[n:]
		num, wireType := tag>>3, tag&7
		var payload []byte
		switch wireType {
		case wireVarint:
			_, m := binary.Uvarint(b)
			if m <= 0 {
				return nil, false, errMalformed
			}
			payload, b = b[:m], b[m:]
		case wireI64:
			if len(b) < 8 {
				return nil, false, errMalformed
			}
			payload, b = b[:8], b[8:]
		case wireI32:
			if len(b) < 4 {
				return nil, false, errMalformed
			}
			payload, b = b[:4], b[4:]
		case wireBytes:
			size, m := binary.Uvarint(b)
			if m <= 0 || uint64(len(b)-m) < size {
				return nil, false, errMalformed
			}
			payload, b = b[m:m+int(size)], b[m+int(size):]
		default:
			return nil, false, fmt.Errorf("%w: wire type %d", errMalformed, wireType)
		}
		replacement, changed, err := fn(num, wireType, payload)
		if err != nil {
			return nil, false, err
		}
		if changed {
			out = append(out, replacement...)
			changedAny = true
		} else {
			out = append(out, start[:len(start)-len(b)]...)
		}
	}
	return out, changedAny, nil
}

func appendTag(b []byte, num, wireType uint64) []byte {
	return binary.AppendUvarint(b, num<<3|wireType)
}

func appendBytesField(b []byte, num uint64, payload []byte) []byte {
	b = appendTag(b, num, wireBytes)
	b = binary.AppendUvarint(b, uint64(len(payload)))
	return append(b, payload...)
}

func encodeValueRef(ref valueRefPair) []byte {
	var b []byte
	b = appendTag(b, 1, wireVarint)
	b = binary.AppendUvarint(b, ref.id)
	b = appendTag(b, 2, wireVarint)
	return binary.AppendUvarint(b, ref.version)
}

func nested(children map[uint64]func([]byte) ([]byte, bool, error)) fieldRewrite {
	return func(num, wireType uint64, payload []byte) ([]byte, bool, error) {
		child, ok := children[num]
		if !ok || wireType != wireBytes {
			return nil, false, nil
		}
		inner, changed, err := child(payload)
		if err != nil || !changed {
			return nil, false, err
		}
		return appendBytesField(nil, num, inner), true, nil
	}
}

func messageRewriter(fn fieldRewrite) func([]byte) ([]byte, bool, error) {
	return func(b []byte) ([]byte, bool, error) { return rewriteMessage(b, fn) }
}

func (r *valueRefRewriter) remap(field string, refs map[uint64]valueRefPair, oldTag, newTag uint64) func(uint64, uint64, []byte) ([]byte, bool, error) {
	return r.remapOr(field, refs, oldTag, newTag, 0)
}

func (r *valueRefRewriter) remapOr(field string, refs map[uint64]valueRefPair, oldTag, newTag, literalTag uint64) func(uint64, uint64, []byte) ([]byte, bool, error) {
	return func(num, wireType uint64, payload []byte) ([]byte, bool, error) {
		if num != oldTag || wireType != wireVarint {
			return nil, false, nil
		}
		rowID, _ := binary.Uvarint(payload)
		ref, ok := refs[rowID]
		if !ok && literalTag != 0 && r.historical {
			r.unknown = append(r.unknown, fmt.Sprintf("%s row id %d", field, rowID))
			return appendBytesField(nil, literalTag, []byte(unknownValueRef)), true, nil
		}
		if !ok {
			panic(fmt.Sprintf("value reference migration: %s row %d: %s references row id %d, which is not a value version", r.table, r.rowID, field, rowID))
		}
		return appendBytesField(nil, newTag, encodeValueRef(ref)), true, nil
	}
}

func chain(fns ...fieldRewrite) fieldRewrite {
	return func(num, wireType uint64, payload []byte) ([]byte, bool, error) {
		for _, fn := range fns {
			out, changed, err := fn(num, wireType, payload)
			if err != nil || changed {
				return out, changed, err
			}
		}
		return nil, false, nil
	}
}

func (r *valueRefRewriter) deployment(b []byte) ([]byte, bool, error) {
	envVar := messageRewriter(chain(
		r.remapOr("env var secret", r.maps.secrets, 1, 8, 3),
		r.remapOr("env var config", r.maps.configs, 2, 9, 3),
		r.remapOr("env var asset", r.maps.assets, 5, 10, 3),
	))
	envEntry := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){2: envVar}))
	assetMount := messageRewriter(r.remap("asset mount", r.maps.assets, 1, 4))
	runtime := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){2: envEntry, 8: assetMount}))
	container := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){2: runtime}))

	secretCert := messageRewriter(r.remap("ingress cert secret", r.maps.secrets, 1, 2))
	certSource := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){2: secretCert}))
	httpsConfig := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){7: certSource}))
	ingress := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){4: httpsConfig}))
	networking := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){3: ingress}))

	spec := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){1: networking, 2: container, 3: container, 4: container}))
	return rewriteMessage(b, nested(map[uint64]func([]byte) ([]byte, bool, error){8: spec}))
}

func (r *valueRefRewriter) systemConfig(b []byte) ([]byte, bool, error) {
	secretRef := func(field string) func([]byte) ([]byte, bool, error) {
		return messageRewriter(r.remap(field, r.maps.secrets, 3, 4))
	}
	setting := func(field string) func([]byte) ([]byte, bool, error) {
		configRef := messageRewriter(r.remap(field, r.maps.configs, 3, 4))
		return messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){2: configRef}))
	}
	group := func(name string, fields map[uint64]string, secrets map[uint64]string) func([]byte) ([]byte, bool, error) {
		children := map[uint64]func([]byte) ([]byte, bool, error){}
		for num, field := range fields {
			children[num] = setting(name + "." + field)
		}
		for num, field := range secrets {
			children[num] = secretRef(name + "." + field)
		}
		return messageRewriter(nested(children))
	}
	settings := messageRewriter(nested(map[uint64]func([]byte) ([]byte, bool, error){
		1: group("http_web", map[uint64]string{1: "enabled", 2: "listen"}, nil),
		2: group("https_web", map[uint64]string{1: "enabled", 2: "listen", 3: "tls_self_managed", 5: "acme_hosts", 6: "acme_email"}, map[uint64]string{4: "tls_cert_pem"}),
		3: group("cluster", map[uint64]string{1: "listen", 2: "enrollment_listen"}, nil),
		4: group("repo", nil, map[uint64]string{1: "github_token"}),
		5: group("backup", map[uint64]string{1: "enabled", 2: "s3_access_key_id", 4: "s3_bucket", 5: "s3_path", 6: "s3_region", 7: "s3_endpoint"}, map[uint64]string{3: "s3_secret_access_key"}),
		6: group("large_assets", map[uint64]string{1: "use_separate_s3", 2: "s3_access_key_id", 4: "s3_bucket", 5: "s3_path", 6: "s3_region", 7: "s3_endpoint", 8: "keep_local_copy"}, map[uint64]string{3: "s3_secret_access_key"}),
		7: group("auth", map[uint64]string{1: "password_login_enabled"}, nil),
	}))
	return rewriteMessage(b, nested(map[uint64]func([]byte) ([]byte, bool, error){1: settings}))
}
