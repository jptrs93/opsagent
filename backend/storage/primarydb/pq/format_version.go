package pq

import (
	"database/sql"
	"errors"
	"fmt"
)

const DataModelFormatVersion = 2

const formatRefusal = "this database predates the data model contract: start it on v0.0.616 once before upgrading (a database from before v0.0.615 has to start on v0.0.615 first)"

func requireDataModelFormat(db *sql.DB) {
	var tables int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_litestream_%'`).Scan(&tables); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if tables == 0 {
		return
	}
	var present int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'format_version'`).Scan(&present); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if present == 0 {
		panic(formatRefusal)
	}
	var version int64
	switch err := db.QueryRow(`SELECT version FROM format_version WHERE id = 1`).Scan(&version); {
	case errors.Is(err, sql.ErrNoRows):
		panic(formatRefusal)
	case err != nil:
		panic(fmt.Errorf("format check: %w", err))
	case version < DataModelFormatVersion:
		panic(formatRefusal)
	case version > DataModelFormatVersion:
		panic(fmt.Sprintf("this database was written by a newer release (format %d, this binary reads %d)", version, DataModelFormatVersion))
	}
}

func markFormatVersion(db *sql.DB) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO format_version (id, version) VALUES (1, ?)`, DataModelFormatVersion); err != nil {
		panic(fmt.Errorf("format version: %w", err))
	}
}
