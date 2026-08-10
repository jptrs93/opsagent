package primarydb

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAssetMigrationLifecycleAndConfigTransaction(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	oldID, err := store.AppendOpenDeploySettings([]byte("old"))
	if err != nil {
		t.Fatalf("append old config: %v", err)
	}
	newID, migration, err := store.AppendOpenDeploySettingsWithAssetMigration([]byte("new"), true)
	if err != nil {
		t.Fatalf("append new config and migration: %v", err)
	}
	if migration == nil || migration.OldConfigVersionID != oldID || migration.NewConfigVersionID != newID || migration.Status != "pending" {
		t.Fatalf("migration = %+v, old=%d new=%d", migration, oldID, newID)
	}
	if _, _, err := store.AppendOpenDeploySettingsWithAssetMigration([]byte("blocked"), false); !errors.Is(err, ErrAssetMigrationInProgress) {
		t.Fatalf("second settings append error = %v, want ErrAssetMigrationInProgress", err)
	}
	latest, err := store.FetchLatestOpenDeployConfig()
	if err != nil || latest.ID != newID {
		t.Fatalf("latest config after blocked append = %+v, err=%v", latest, err)
	}

	running := store.StartAssetMigration(migration.ID)
	if running.Status != "running" || running.StartedAt == 0 {
		t.Fatalf("running migration = %+v", running)
	}
	running = store.RecordAssetMigrationError(migration.ID, errors.New("temporary"))
	if running.Status != "running" || running.LastError != "temporary" {
		t.Fatalf("errored migration = %+v", running)
	}
	finished := store.FinishAssetMigration(migration.ID)
	if finished.Status != "finished" || finished.FinishedAt == 0 || finished.LastError != "" {
		t.Fatalf("finished migration = %+v", finished)
	}
	if _, ok := store.GetUnfinishedAssetMigration(); ok {
		t.Fatal("finished migration was returned as unfinished")
	}
}
