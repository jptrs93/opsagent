package systemconfig

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func testConfigBlob(prefix []byte, hash string) []byte {
	cfg := Default(DefaultInitial())
	cfg.MasterPasswordHash = apigen.Some(hash)
	cfg.NetworkUlaPrefix = prefix
	return cfg.Encode()
}

func TestAppendRevisionRunsValidationInsideTheCommit(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	prefix := network.GeneratePrefix().Bytes()
	oldID, err := AppendRevision(store, 0, testConfigBlob(prefix, "old"), nil)
	if err != nil {
		t.Fatalf("append old config: %v", err)
	}
	rejected := errors.New("rejected")
	if _, err := AppendRevision(store, 0, testConfigBlob(prefix, "blocked"), func(*pq.Queries) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("append with failing validation error = %v, want %v", err, rejected)
	}
	latest, err := LatestRevision(store.Queries())
	if err != nil || latest.Seq != oldID {
		t.Fatalf("latest config after rejected append = %+v, err=%v", latest, err)
	}
}

func TestRevisionWritersPublishSequencedPersistedState(t *testing.T) {
	s := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(kind apigen.AuthzVerb) {
		t.Helper()
		update := <-sub
		statetest.AssertUpdateMatchesRows(t, s, update)
		row, err := LatestRevision(s.Queries())
		if err != nil {
			t.Fatal(err)
		}
		stored := erru.Must(apigen.DecodeSystemConfig(row.ConfigBlob))
		if len(update.Mutations) != 1 || update.Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG || update.Mutations[0].Kind() != kind || update.Mutations[0].EntityID() != pq.SystemConfigEntityID {
			t.Fatalf("config publication = %+v, want one %v of the system config", update, kind)
		}
		if !reflect.DeepEqual(update.Mutations[0].Entity().Value.SystemConfig, stored) {
			t.Fatal("config publication differs from the persisted revision")
		}
		if live := statetest.Live(t, s.Queries(), apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG)[pq.SystemConfigEntityID]; live == nil || !reflect.DeepEqual(live.Value.SystemConfig, stored) {
			t.Fatal("bootstrap differs from the persisted revision")
		}
	}
	prefix := network.GeneratePrefix().Bytes()
	if _, err := AppendRevision(s, 0, testConfigBlob(prefix, "test"), nil); err != nil {
		t.Fatal(err)
	}
	check(apigen.AuthzVerb_AUTHZ_VERB_CREATE)
	if _, err := AppendRevision(s, 0, testConfigBlob(prefix, "test"), func(*pq.Queries) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
}
