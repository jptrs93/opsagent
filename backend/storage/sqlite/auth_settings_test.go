package sqlite

import (
	"path/filepath"
	"testing"
)

func TestMasterPasswordHashSettingsRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := NewStorageAdapter(dbPath)

	hash, configured, err := store.FetchMasterPasswordHash()
	if err != nil {
		t.Fatalf("FetchMasterPasswordHash before set: %v", err)
	}
	if configured || hash != "" {
		t.Fatalf("expected unset hash, got configured=%v hash=%q", configured, hash)
	}

	if err := store.SetMasterPasswordHash("hash-1"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	hash, configured, err = store.FetchMasterPasswordHash()
	if err != nil {
		t.Fatalf("FetchMasterPasswordHash after set: %v", err)
	}
	if !configured || hash != "hash-1" {
		t.Fatalf("expected configured hash-1, got configured=%v hash=%q", configured, hash)
	}

	reopened := NewStorageAdapter(dbPath)
	hash, configured, err = reopened.FetchMasterPasswordHash()
	if err != nil {
		t.Fatalf("FetchMasterPasswordHash after reopen: %v", err)
	}
	if !configured || hash != "hash-1" {
		t.Fatalf("expected persisted hash-1, got configured=%v hash=%q", configured, hash)
	}
}
