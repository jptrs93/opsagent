package config

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestMasterPasswordHashRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service := &Service{Storage: sqlite.NewPrimaryStorage(dbPath)}

	if err := service.SetMasterPasswordHash("hash-1"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	hash, err := service.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after set: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected hash-1, got %q", hash)
	}

	reopened := &Service{Storage: sqlite.NewPrimaryStorage(dbPath)}
	hash, err = reopened.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after reopen: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected persisted hash-1, got %q", hash)
	}
}
