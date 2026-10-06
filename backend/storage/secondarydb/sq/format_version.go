package sq

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const DataModelFormatVersion = 2

const formatVersionKey = "format_version"

const formatRefusal = "this database predates the data model contract: start this node on v0.0.616 once before upgrading"

func requireDataModelFormat(db *sql.DB) {
	var tables int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if tables == 0 {
		return
	}
	var present int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'local_kv'`).Scan(&present); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if present == 0 {
		panic(formatRefusal)
	}
	var raw []byte
	switch err := db.QueryRow(`SELECT value FROM local_kv WHERE key = ?`, formatVersionKey).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		panic(formatRefusal)
	case err != nil:
		panic(fmt.Errorf("format check: %w", err))
	}
	version, err := strconv.Atoi(string(raw))
	if err != nil {
		panic(fmt.Errorf("format check: local_kv %s = %q", formatVersionKey, raw))
	}
	if version < DataModelFormatVersion {
		panic(formatRefusal)
	}
	if version > DataModelFormatVersion {
		panic(fmt.Sprintf("this database was written by a newer release (format %d, this binary reads %d)", version, DataModelFormatVersion))
	}
}

func markFormatVersion(db *sql.DB) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO local_kv (key, value) VALUES (?, ?)`, formatVersionKey, []byte(strconv.Itoa(DataModelFormatVersion))); err != nil {
		panic(fmt.Errorf("format version: %w", err))
	}
}
