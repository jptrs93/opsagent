package pq

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

func migrateValueEventLogs(db *sql.DB) {
	migrateSecretEventLog(db)
	migrateConfigEventLog(db)
	migrateAssetEventLog(db)
}

type migIdentity struct {
	id        int64
	name      string
	dirID     int64
	createdAt int64
	deletedAt int64
}

type migVersionRow struct {
	id        int64
	version   int64
	createdAt int64
	author    int64
	globalSeq int64
	payload   []any
}

type migSpaceRow struct {
	createdAt int64
	author    int64
	globalSeq int64
	spaceID   int64
}

type migEvent struct {
	id           int64
	globalSeq    int64
	eventTime    int64
	createdTime  int64
	author       int64
	entityID     int64
	version      int64
	valueVersion int64
	spaceVersion int64
	name         string
	dirID        int64
	spaceID      int64
	payload      []any
	eventType    int64
}

func legacyMigrationNeeded(db *sql.DB, identityTable, eventLog string) bool {
	ctx := context.Background()
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, identityTable).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		panic(fmt.Sprintf("event log migration: check %s: %v", identityTable, err))
	}
	var events int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+eventLog).Scan(&events); err != nil {
		panic(fmt.Sprintf("event log migration: count %s: %v", eventLog, err))
	}
	return events == 0
}

func loadMigIdentities(db *sql.DB, query string) []migIdentity {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		panic(fmt.Sprintf("event log migration: identities: %v", err))
	}
	defer rows.Close()
	var out []migIdentity
	for rows.Next() {
		var ident migIdentity
		if err := rows.Scan(&ident.id, &ident.name, &ident.dirID, &ident.createdAt, &ident.deletedAt); err != nil {
			panic(fmt.Sprintf("event log migration: scan identity: %v", err))
		}
		out = append(out, ident)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("event log migration: identities: %v", err))
	}
	return out
}

func loadMigVersionRows(db *sql.DB, query string, payloadColumns int) map[int64][]migVersionRow {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		panic(fmt.Sprintf("event log migration: versions: %v", err))
	}
	defer rows.Close()
	out := make(map[int64][]migVersionRow)
	for rows.Next() {
		var entityID int64
		var r migVersionRow
		r.payload = make([]any, payloadColumns)
		dest := []any{&entityID, &r.id, &r.version, &r.createdAt, &r.author, &r.globalSeq}
		for i := range r.payload {
			dest = append(dest, &r.payload[i])
		}
		if err := rows.Scan(dest...); err != nil {
			panic(fmt.Sprintf("event log migration: scan version: %v", err))
		}
		out[entityID] = append(out[entityID], r)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("event log migration: versions: %v", err))
	}
	return out
}

func loadMigSpaceRows(db *sql.DB, query string) map[int64][]migSpaceRow {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		panic(fmt.Sprintf("event log migration: spaces: %v", err))
	}
	defer rows.Close()
	out := make(map[int64][]migSpaceRow)
	for rows.Next() {
		var entityID int64
		var r migSpaceRow
		if err := rows.Scan(&entityID, &r.createdAt, &r.author, &r.globalSeq, &r.spaceID); err != nil {
			panic(fmt.Sprintf("event log migration: scan space: %v", err))
		}
		out[entityID] = append(out[entityID], r)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("event log migration: spaces: %v", err))
	}
	return out
}

func buildValueEntityEvents(ident migIdentity, versions []migVersionRow, spaces []migSpaceRow, payloadColumns int) []migEvent {
	if len(versions) == 0 {
		return nil
	}
	spaceID := int64(1)
	if len(spaces) > 0 {
		spaceID = spaces[0].spaceID
		spaces = spaces[1:]
	}
	type step struct {
		version migVersionRow
		space   migSpaceRow
		isSpace bool
	}
	var steps []step
	vi, si := 0, 0
	for vi < len(versions) || si < len(spaces) {
		takeVersion := vi < len(versions)
		if takeVersion && si < len(spaces) {
			v, sp := versions[vi], spaces[si]
			if sp.globalSeq < v.globalSeq ||
				(sp.globalSeq == v.globalSeq && sp.createdAt < v.createdAt) {
				takeVersion = false
			}
		}
		if takeVersion {
			steps = append(steps, step{version: versions[vi]})
			vi++
		} else {
			steps = append(steps, step{space: spaces[si], isSpace: true})
			si++
		}
	}

	var out []migEvent
	var valueVersion int64
	spaceVersion := int64(1)
	for i, st := range steps {
		e := migEvent{
			entityID:    ident.id,
			version:     int64(i) + 1,
			createdTime: ident.createdAt,
			name:        ident.name,
			dirID:       ident.dirID,
			eventType:   EventUpdate,
		}
		if i == 0 {
			e.eventType = EventCreate
		}
		if st.isSpace {
			spaceVersion++
			spaceID = st.space.spaceID
			e.globalSeq = st.space.globalSeq
			e.eventTime = st.space.createdAt
			e.author = st.space.author
		} else {
			valueVersion = st.version.version
			e.id = st.version.id
			e.globalSeq = st.version.globalSeq
			e.eventTime = st.version.createdAt
			e.author = st.version.author
			e.payload = st.version.payload
		}
		e.valueVersion = valueVersion
		e.spaceVersion = spaceVersion
		e.spaceID = spaceID
		out = append(out, e)
	}
	if ident.deletedAt != 0 {
		out = append(out, migEvent{
			eventTime:    ident.deletedAt,
			createdTime:  ident.createdAt,
			entityID:     ident.id,
			version:      int64(len(out)) + 1,
			valueVersion: valueVersion,
			spaceVersion: spaceVersion,
			name:         ident.name,
			dirID:        ident.dirID,
			spaceID:      spaceID,
			eventType:    EventDelete,
		})
	}
	for i := range out {
		if out[i].payload == nil {
			out[i].payload = make([]any, payloadColumns)
		}
	}
	return out
}

func insertMigEvents(db *sql.DB, what, insertSQL string, events []migEvent) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("%s migration: begin tx: %v", what, err))
	}
	defer tx.Rollback()
	sort.SliceStable(events, func(i, j int) bool {
		return (events[i].id != 0) && (events[j].id == 0)
	})
	for _, e := range events {
		var id any
		if e.id != 0 {
			id = e.id
		}
		args := []any{id, e.globalSeq, e.eventTime, e.createdTime, e.author,
			e.entityID, e.version, e.valueVersion, e.spaceVersion,
			e.name, e.dirID, e.spaceID}
		args = append(args, e.payload...)
		args = append(args, e.eventType)
		if _, err := tx.ExecContext(ctx, insertSQL, args...); err != nil {
			panic(fmt.Sprintf("%s migration: insert entity=%d v=%d: %v", what, e.entityID, e.version, err))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("%s migration: commit: %v", what, err))
	}
}

func migrateValueEntity(db *sql.DB, what string, payloadColumns int, identityQuery, versionQuery, spaceQuery, insertSQL string) {
	identities := loadMigIdentities(db, identityQuery)
	if len(identities) == 0 {
		return
	}
	versions := loadMigVersionRows(db, versionQuery, payloadColumns)
	spaces := loadMigSpaceRows(db, spaceQuery)
	var events []migEvent
	for _, ident := range identities {
		events = append(events, buildValueEntityEvents(ident, versions[ident.id], spaces[ident.id], payloadColumns)...)
	}
	insertMigEvents(db, what, insertSQL, events)
}

func migrateSecretEventLog(db *sql.DB) {
	if !legacyMigrationNeeded(db, "secrets", "secret_event_log") {
		return
	}
	migrateValueEntity(db, "secret event log", 3,
		`SELECT id, name, value_directory_id, created_at, deleted_at FROM secrets ORDER BY id`,
		`SELECT secret_id, id, version, created_at, author, global_seq, smk_version, ciphertext, nonce
		 FROM secret_versions ORDER BY secret_id, version`,
		`SELECT secret_id, created_at, author, global_seq, space_id FROM secret_spaces ORDER BY secret_id, id`,
		`INSERT INTO secret_event_log (
			id, global_seq, event_time, created_time, author, secret_id, version,
			value_version, space_version, name, value_directory_id, space_id,
			smk_version, ciphertext, nonce, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
}

func migrateConfigEventLog(db *sql.DB) {
	if !legacyMigrationNeeded(db, "configs", "config_event_log") {
		return
	}
	migrateValueEntity(db, "config event log", 1,
		`SELECT id, name, value_directory_id, created_at, deleted_at FROM configs ORDER BY id`,
		`SELECT config_id, id, version, created_at, author, global_seq, value
		 FROM config_versions ORDER BY config_id, version`,
		`SELECT config_id, created_at, author, global_seq, space_id FROM config_spaces ORDER BY config_id, id`,
		`INSERT INTO config_event_log (
			id, global_seq, event_time, created_time, author, config_id, version,
			value_version, space_version, name, value_directory_id, space_id,
			value, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
}

func migrateAssetEventLog(db *sql.DB) {
	if !legacyMigrationNeeded(db, "assets", "asset_event_log") {
		return
	}
	migrateValueEntity(db, "asset event log", 2,
		`SELECT id, key, asset_directory_id, created_at, deleted_at FROM assets ORDER BY id`,
		`SELECT asset_id, id, version, created_at, author, global_seq, size_bytes, sha256
		 FROM asset_versions ORDER BY asset_id, version`,
		`SELECT asset_id, created_at, author, global_seq, space_id FROM asset_spaces ORDER BY asset_id, id`,
		`INSERT INTO asset_event_log (
			id, global_seq, event_time, created_time, author, asset_id, version,
			value_version, space_version, key, asset_directory_id, space_id,
			size_bytes, sha256, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
}
