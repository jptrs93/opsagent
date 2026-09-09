package systemconfig

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestAssetMigrationLifecycleAndConfigTransaction(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	q := store.Queries()
	oldID, err := AppendRevision(store, (&apigen.SystemConfig{MasterPasswordHash: "old"}).Encode())
	if err != nil {
		t.Fatalf("append old config: %v", err)
	}
	newID, migration, err := AppendRevisionWithAssetMigration(store, (&apigen.SystemConfig{MasterPasswordHash: "new"}).Encode(), true, nil)
	if err != nil {
		t.Fatalf("append new config and migration: %v", err)
	}
	if migration == nil || migration.OldConfigVersionID != oldID || migration.NewConfigVersionID != newID || migration.Status != "pending" {
		t.Fatalf("migration = %+v, old=%d new=%d", migration, oldID, newID)
	}
	if _, _, err := AppendRevisionWithAssetMigration(store, (&apigen.SystemConfig{MasterPasswordHash: "blocked"}).Encode(), false, nil); !errors.Is(err, ErrAssetMigrationInProgress) {
		t.Fatalf("second settings append error = %v, want ErrAssetMigrationInProgress", err)
	}
	latest, err := LatestRevision(q)
	if err != nil || latest.ID != newID {
		t.Fatalf("latest config after blocked append = %+v, err=%v", latest, err)
	}
	running, err := q.StartAssetMigration(ctx, pq.StartAssetMigrationParams{StartedAt: 1, LastAttemptAt: 1, ID: migration.ID})
	if err != nil || running.Status != "running" || running.StartedAt == 0 {
		t.Fatalf("running migration = %+v, %v", running, err)
	}
	running, err = q.RecordAssetMigrationError(ctx, pq.RecordAssetMigrationErrorParams{LastAttemptAt: 2, LastError: "temporary", ID: migration.ID})
	if err != nil || running.Status != "running" || running.LastError != "temporary" {
		t.Fatalf("errored migration = %+v, %v", running, err)
	}
	finished, err := q.FinishAssetMigration(ctx, pq.FinishAssetMigrationParams{FinishedAt: 3, ID: migration.ID})
	if err != nil || finished.Status != "finished" || finished.FinishedAt == 0 || finished.LastError != "" {
		t.Fatalf("finished migration = %+v, %v", finished, err)
	}
	if _, ok := UnfinishedAssetMigration(q); ok {
		t.Fatal("finished migration was returned as unfinished")
	}
}

func TestRevisionWritersPublishSequencedPersistedState(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func() {
		t.Helper()
		update := <-sub
		statetest.AssertUpdateMatchesRows(t, s, update)
		snapshot := s.BuildSnapshot(context.Background())
		if update.SystemConfig == nil || !reflect.DeepEqual(update.SystemConfig, snapshot.SystemConfig) {
			t.Fatal("config publication differs from persisted public state")
		}
	}
	if _, err := AppendRevision(s, (&apigen.SystemConfig{MasterPasswordHash: "test"}).Encode()); err != nil {
		t.Fatal(err)
	}
	check()
	if _, _, err := AppendRevisionWithAssetMigration(s, (&apigen.SystemConfig{MasterPasswordHash: "test"}).Encode(), true, nil); err != nil {
		t.Fatal(err)
	}
	check()
}
