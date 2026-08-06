package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Existing installs already have an agent_sessions table without the lifecycle
// columns, and CREATE TABLE IF NOT EXISTS will not add them, so the migration
// is the only thing that can. Rows written before it must come back as live
// approved sessions, not as status 0.
func TestAgentSessionsMigrationBackfillsExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE agent_sessions (
        id TEXT PRIMARY KEY, user_id INTEGER NOT NULL, created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL, token_hash BLOB NOT NULL, token_prefix TEXT NOT NULL,
        revoked_at INTEGER NOT NULL DEFAULT 0, scopes TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("creating legacy table: %v", err)
	}
	now := time.Now()
	if _, err := db.Exec(
		`INSERT INTO agent_sessions VALUES (?,?,?,?,?,?,?,?), (?,?,?,?,?,?,?,?)`,
		"live", 1, now.Unix(), now.Add(time.Hour).Unix(), []byte("hash"), "prefix", 0, "default",
		"dead", 1, now.Unix(), now.Add(time.Hour).Unix(), []byte("hash"), "prefix", now.Unix(), "default",
	); err != nil {
		t.Fatalf("seeding legacy rows: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Twice, because migrations re-run on every startup and must be idempotent.
	for range 2 {
		store := NewPrimaryStorage(path)
		records, err := store.ListAgentSessionsForUser(1)
		if err != nil {
			t.Fatalf("ListAgentSessionsForUser: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("got %d rows, want 2", len(records))
		}
		byID := map[string]AgentSessionRecord{}
		for _, rec := range records {
			byID[rec.ID] = rec
		}
		if got := byID["live"].Status; got != apigen.AgentSessionStatus_AGENT_SESSION_APPROVED {
			t.Fatalf("pre-existing live session status = %v, want APPROVED", got)
		}
		if got := byID["dead"].Status; got != apigen.AgentSessionStatus_AGENT_SESSION_REVOKED {
			t.Fatalf("pre-existing revoked session status = %v, want REVOKED", got)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing store: %v", err)
		}
	}
}
