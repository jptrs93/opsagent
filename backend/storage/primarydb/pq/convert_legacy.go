package pq

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
	"github.com/jptrs93/opsagent/backend/storage/legacyconv"
)

// DataModelFormatVersion is the write log payload format this release reads.
// Version 1 is the v0.0.615 log encoded under api-contract-old; version 2 is
// the contract rewritten from the data model. A database carrying no
// format_version row is a version 1 log and is converted once at open.
const DataModelFormatVersion = 2

const preConversionSuffix = ".pre-datamodel-conversion"

var keptAcrossConversion = map[string]bool{
	"write_events": true, "write_event_mutations": true, "asset_store": true, "global_seq": true,
	"entity_ids": true, "format_version": true, "sqlite_sequence": true,
}

// Refusal is one logged payload the conversion has no rule for.
type Refusal struct {
	Seq, Idx int64
	Type     apigen.CoreEntityType
	EntityID uint64
	Err      error
}

func (r Refusal) String() string {
	return fmt.Sprintf("seq %d idx %d %v %d: %v", r.Seq, r.Idx, r.Type, r.EntityID, r.Err)
}

// ConversionReport is what a pass over a version 1 log found.
type ConversionReport struct {
	Converted  map[apigen.CoreEntityType]int
	Deletes    int
	Counters   int
	KeyslotIDs map[uint64]uint64
	Repairs    []string
	Refusals   []Refusal
}

type convertedRow struct {
	seq, idx int64
	entityID uint64
	payload  []byte
	counters *loggedDeploymentFacts
}

func legacyDataModel(db *sql.DB) bool {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'write_event_mutations'`).Scan(&n); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if n == 0 {
		return false
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'format_version'`).Scan(&n); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if n == 0 {
		return true
	}
	var version int64
	switch err := db.QueryRow(`SELECT version FROM format_version WHERE id = 1`).Scan(&version); {
	case errors.Is(err, sql.ErrNoRows):
		return true
	case err != nil:
		panic(fmt.Errorf("format check: %w", err))
	case version > DataModelFormatVersion:
		panic(fmt.Sprintf("this database was written by a newer release (format %d, this binary reads %d)", version, DataModelFormatVersion))
	}
	return version < DataModelFormatVersion
}

func markFormatVersion(db *sql.DB) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO format_version (id, version) VALUES (1, ?)`, DataModelFormatVersion); err != nil {
		panic(fmt.Errorf("format version: %w", err))
	}
}

// backupBeforeConversion keeps the version 1 file beside the database. An
// existing copy is the one taken before the first attempt and is kept.
func backupBeforeConversion(db *sql.DB, dbPath string) {
	target := dbPath + preConversionSuffix
	if _, err := os.Stat(target); err == nil {
		return
	}
	if _, err := db.Exec(`VACUUM INTO ?`, target); err != nil {
		panic(fmt.Errorf("pre-conversion backup to %s: %w", target, err))
	}
}

// convertDataModel rewrites a version 1 log in place: every payload through
// legacyconv, the deployment counters the old payloads carried into the
// version and spec_version columns, fresh keyslot ids, then the new schema,
// a rebuild of the materialised tables, and the format_version row. It is one
// transaction, so an interrupted conversion leaves the version 1 database as
// it was.
func convertDataModel(ctx context.Context, db *sql.DB) *ConversionReport {
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	defer tx.Rollback()
	if err := addCounterColumns(ctx, tx); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	report, rows, err := convertLog(ctx, tx)
	if err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	if len(report.Refusals) > 0 {
		for _, r := range report.Refusals {
			slog.ErrorContext(ctx, "data model conversion refused a logged payload", "refusal", r.String())
		}
		panic(fmt.Sprintf("data model conversion: %d logged payloads have no representation in the new model; the first is %s", len(report.Refusals), report.Refusals[0]))
	}
	for _, repair := range report.Repairs {
		slog.WarnContext(ctx, "data model conversion repaired a historic payload", "repair", repair)
	}
	for _, row := range rows {
		var version, specVersion any
		if row.counters != nil {
			version, specVersion = row.counters.version, row.counters.specVersion
		}
		if _, err := tx.ExecContext(ctx, `UPDATE write_event_mutations SET entity_id = ?, payload = ?, version = ?, spec_version = ? WHERE seq = ? AND idx = ?`,
			row.entityID, row.payload, version, specVersion, row.seq, row.idx); err != nil {
			panic(fmt.Errorf("data model conversion: seq %d idx %d: %w", row.seq, row.idx, err))
		}
	}
	if err := dropTablesExcept(ctx, tx, keptAcrossConversion); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	if err := applySchemaTx(ctx, tx); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	q := &Queries{db: &conn{DBTX: tx}, applied: map[*apigen.CoreWriteUpdate]int{}}
	if err := q.RebuildFromLog(ctx); err != nil {
		panic(fmt.Errorf("data model conversion: rebuild: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO format_version (id, version) VALUES (1, ?)`, DataModelFormatVersion); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	slog.InfoContext(ctx, fmt.Sprintf("write log converted to the data model contract in %s", time.Since(start).Round(time.Millisecond)),
		"payloads", len(rows), "repairs", len(report.Repairs), "keyslots", len(report.KeyslotIDs))
	return report
}

// DryRunConversion converts every payload of a version 1 log without writing
// anything and reports what it found. A database already on the current
// format reports nothing to convert.
func DryRunConversion(ctx context.Context, db *sql.DB) (*ConversionReport, error) {
	if !legacyDataModel(db) {
		return &ConversionReport{Converted: map[apigen.CoreEntityType]int{}, KeyslotIDs: map[uint64]uint64{}}, nil
	}
	report, _, err := convertLog(ctx, db)
	return report, err
}

func addCounterColumns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(write_event_mutations)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var cid int64
		var name, typ string
		var notNull, pk int64
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"version", "spec_version"} {
		if present[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE write_event_mutations ADD COLUMN `+column+` INTEGER`); err != nil {
			return err
		}
	}
	return nil
}

func convertLog(ctx context.Context, db DBTX) (*ConversionReport, []convertedRow, error) {
	report := &ConversionReport{Converted: map[apigen.CoreEntityType]int{}, KeyslotIDs: map[uint64]uint64{}}
	templates := map[uint64]*apigen.AuthzGrantTemplate{}
	lookup := legacyconv.TemplateLookup(func(id uint64) (*apigen.AuthzGrantTemplate, bool) {
		t, ok := templates[id]
		return t, ok
	})
	keyslotID := func(old uint64) uint64 {
		if id, ok := report.KeyslotIDs[old]; ok {
			return id
		}
		id := uint64(len(report.KeyslotIDs) + 1)
		report.KeyslotIDs[old] = id
		return id
	}
	latest, ulaPrefix, err := latestMutations(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT seq, idx, entity_type, entity_id, op, payload FROM write_event_mutations ORDER BY seq, idx`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []convertedRow
	for rows.Next() {
		var seq, idx, typ, op int64
		var entityID uint64
		var payload []byte
		if err := rows.Scan(&seq, &idx, &typ, &entityID, &op, &payload); err != nil {
			return nil, nil, err
		}
		t := apigen.CoreEntityType(typ)
		row := convertedRow{seq: seq, idx: idx, entityID: entityID}
		if t == apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT {
			row.entityID = keyslotID(entityID)
		}
		if apigen.AuthzVerb(op) == apigen.AuthzVerb_AUTHZ_VERB_DELETE || payload == nil {
			report.Deletes++
			if row.entityID != entityID {
				out = append(out, row)
			}
			continue
		}
		ids := legacyconv.IDs{
			EntityID:  entityID,
			Keyslot:   func(apigenold.SecretKeyslot) uint64 { return row.entityID },
			Template:  lookup,
			Historic:  latest[mutationKey{t, entityID}] != rowKey{seq, idx},
			UlaPrefix: ulaPrefix,
		}
		entity, repairs, err := legacyconv.Entity(t, payload, ids)
		if err != nil {
			report.Refusals = append(report.Refusals, Refusal{Seq: seq, Idx: idx, Type: t, EntityID: entityID, Err: err})
			continue
		}
		for _, repair := range repairs {
			report.Repairs = append(report.Repairs, fmt.Sprintf("seq %d idx %d %v %d: %s", seq, idx, t, entityID, repair))
		}
		if t == apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE && entity.Value.AuthzGrantTemplate != nil {
			templates[entityID] = entity.Value.AuthzGrantTemplate
		}
		if facts, ok := loggedDeploymentCounters(payload); ok {
			row.counters = &facts
			report.Counters++
		}
		row.payload = entity.Encode()
		report.Converted[t]++
		out = append(out, row)
	}
	return report, out, rows.Err()
}

type mutationKey struct {
	typ apigen.CoreEntityType
	id  uint64
}

type rowKey struct {
	seq, idx int64
}

// latestMutations maps every entity to its last logged row, so the walk knows
// which payloads are history, and reads the ULA prefix of the latest system
// config revision for the historic revisions that predate it.
func latestMutations(ctx context.Context, db DBTX) (map[mutationKey]rowKey, []byte, error) {
	rows, err := db.QueryContext(ctx, `SELECT entity_type, entity_id, seq, idx FROM write_event_mutations m
WHERE NOT EXISTS (SELECT 1 FROM write_event_mutations n WHERE n.entity_type = m.entity_type AND n.entity_id = m.entity_id AND (n.seq > m.seq OR (n.seq = m.seq AND n.idx > m.idx)))`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	latest := map[mutationKey]rowKey{}
	for rows.Next() {
		var typ int64
		var id uint64
		var key rowKey
		if err := rows.Scan(&typ, &id, &key.seq, &key.idx); err != nil {
			return nil, nil, err
		}
		latest[mutationKey{apigen.CoreEntityType(typ), id}] = key
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var ulaPrefix []byte
	var payload []byte
	err = db.QueryRowContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND payload IS NOT NULL ORDER BY seq DESC, idx DESC LIMIT 1`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG)).Scan(&payload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, nil, err
	default:
		if old, err := apigenold.DecodeCoreEntity(payload); err == nil && old.SystemConfig != nil {
			ulaPrefix = old.SystemConfig.NetworkUlaPrefix
		}
	}
	return latest, ulaPrefix, nil
}

func dropTablesExcept(ctx context.Context, tx *sql.Tx, keep map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '\_litestream%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !keep[name] {
			drop = append(drop, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range drop {
		if _, err := tx.ExecContext(ctx, `DROP TABLE "`+name+`"`); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
	}
	return nil
}

func applySchemaTx(ctx context.Context, tx *sql.Tx) error {
	names, err := fs.Glob(schemaFiles, "sql/schema*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		stmts, err := fs.ReadFile(schemaFiles, name)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(stmts)); err != nil {
			return fmt.Errorf("exec %s: %w", name, err)
		}
	}
	return nil
}

// loggedDeploymentCounters reads the version and spec_version the v0.0.614
// backfill wrote into every deployment payload at Deployment tags 15 and 16
// of the old contract. The spec counter cannot be re-derived from the
// payloads: specs that differed only in fields dropped before the backfill
// decode equal now, so the conversion lifts the numbers into the columns the
// rebuild reads.
func loggedDeploymentCounters(payload []byte) (loggedDeploymentFacts, bool) {
	deployment, ok := protoField(payload, uint64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT))
	if !ok {
		return loggedDeploymentFacts{}, false
	}
	version, okVersion := protoField(deployment.bytes, 15)
	spec, okSpec := protoField(deployment.bytes, 16)
	if !okVersion || !okSpec || version.varint == 0 || spec.varint == 0 {
		return loggedDeploymentFacts{}, false
	}
	return loggedDeploymentFacts{version: uint32(version.varint), specVersion: uint32(spec.varint)}, true
}

type protoValue struct {
	varint uint64
	bytes  []byte
}

func protoField(buf []byte, want uint64) (protoValue, bool) {
	for len(buf) > 0 {
		key, n := binary.Uvarint(buf)
		if n <= 0 {
			return protoValue{}, false
		}
		buf = buf[n:]
		tag, wire := key>>3, key&7
		var v protoValue
		switch wire {
		case 0:
			x, n := binary.Uvarint(buf)
			if n <= 0 {
				return protoValue{}, false
			}
			v.varint, buf = x, buf[n:]
		case 1:
			if len(buf) < 8 {
				return protoValue{}, false
			}
			v.bytes, buf = buf[:8], buf[8:]
		case 2:
			l, n := binary.Uvarint(buf)
			if n <= 0 || uint64(len(buf)-n) < l {
				return protoValue{}, false
			}
			v.bytes, buf = buf[n:n+int(l)], buf[n+int(l):]
		case 5:
			if len(buf) < 4 {
				return protoValue{}, false
			}
			v.bytes, buf = buf[:4], buf[4:]
		default:
			return protoValue{}, false
		}
		if tag == want {
			return v, true
		}
	}
	return protoValue{}, false
}
