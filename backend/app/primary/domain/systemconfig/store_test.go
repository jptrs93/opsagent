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

func TestAppendRevisionRunsValidationInsideTheCommit(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	oldID, err := AppendRevision(store, 0, (&apigen.SystemConfig{MasterPasswordHash: "old"}).Encode(), nil)
	if err != nil {
		t.Fatalf("append old config: %v", err)
	}
	rejected := errors.New("rejected")
	if _, err := AppendRevision(store, 0, (&apigen.SystemConfig{MasterPasswordHash: "blocked"}).Encode(), func(*pq.Queries) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("append with failing validation error = %v, want %v", err, rejected)
	}
	latest, err := LatestRevision(store.Queries())
	if err != nil || latest.ID != oldID {
		t.Fatalf("latest config after rejected append = %+v, err=%v", latest, err)
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
	if _, err := AppendRevision(s, 0, (&apigen.SystemConfig{MasterPasswordHash: "test"}).Encode(), nil); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := AppendRevision(s, 0, (&apigen.SystemConfig{MasterPasswordHash: "test"}).Encode(), func(*pq.Queries) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check()
}
