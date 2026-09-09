package values

import (
	"context"
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func openTestStore(t *testing.T) *state.Service {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func setConfigByName(s *state.Service, name, value string, author int32) *apigen.ConfigEvent {
	for _, cfg := range ListConfigs(s.Queries()) {
		if cfg.Value.Fs.Name != name || cfg.SpaceID() != nodes.DefaultSpaceID || cfg.Value.Fs.DirectoryID != 0 {
			continue
		}
		event, _, err := AppendConfigVersion(s, cfg.ConfigID, value, author, false, nil)
		return erru.Must(event, err)
	}
	return erru.Must(CreateConfig(s, name, nodes.DefaultSpaceID, 0, author, value))
}

func latestConfigRef(t *testing.T, c *apigen.ConfigEvent) *statetest.ValueVersion {
	t.Helper()
	if c == nil || c.EventID == 0 {
		t.Fatalf("config event missing: %+v", c)
	}
	return statetest.LatestValue(nil, c)
}

func TestSetUserConfigAtomicallyUpdatesReferencingDeployments(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	database := setConfigByName(store, "database", "one", 1)
	database = setConfigByName(store, "database", "two", 1)
	firstID := statetest.ValueVersions(store, database)[1].ID
	secondID := statetest.ValueVersions(store, database)[0].ID
	unrelated := setConfigByName(store, "other", "keep", 1)
	unrelatedID := latestConfigRef(t, unrelated).ID
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, spec)
	}
	firstDeployment := create("first", statetest.EnvRefSpec(map[string]int32{"DATABASE": firstID, "OTHER": unrelatedID}, nil))
	secondDeployment := create("second", statetest.EnvRefSpec(map[string]int32{"DATABASE": secondID}, nil))
	unchangedDeployment := create("unchanged", statetest.EnvRefSpec(map[string]int32{"OTHER": unrelatedID}, nil))

	saved, updatedIDs, err := AppendConfigVersion(store, database.ConfigID, "three", 9, true, []storage.DeploymentSpecVersion{
		{ID: firstDeployment.DeploymentID, SpecVersion: firstDeployment.SpecVersion},
		{ID: secondDeployment.DeploymentID, SpecVersion: secondDeployment.SpecVersion},
	})
	if err != nil {
		t.Fatalf("set config with deployment updates: %v", err)
	}
	savedRef := latestConfigRef(t, saved)
	if savedRef.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("saved config = %+v, updated deployments = %v", saved, updatedIDs)
	}
	firstCurrent := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(firstDeployment.DeploymentID)))
	secondCurrent := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(secondDeployment.DeploymentID)))
	unchangedCurrent := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(unchangedDeployment.DeploymentID)))
	if got := statetest.DeploymentEnvRefID(t, firstCurrent, "DATABASE", false); got != savedRef.ID {
		t.Fatalf("first deployment config ref = %d, want %d", got, savedRef.ID)
	}
	if got := statetest.DeploymentEnvRefID(t, secondCurrent, "DATABASE", false); got != savedRef.ID {
		t.Fatalf("second deployment config ref = %d, want %d", got, savedRef.ID)
	}
	if got := statetest.DeploymentEnvRefID(t, firstCurrent, "OTHER", false); got != unrelatedID {
		t.Fatalf("unrelated config ref = %d, want %d", got, unrelatedID)
	}
	if firstCurrent.SpecVersion != firstDeployment.SpecVersion+1 || secondCurrent.SpecVersion != secondDeployment.SpecVersion+1 {
		t.Fatalf("updated deployment versions = %d, %d", firstCurrent.SpecVersion, secondCurrent.SpecVersion)
	}
	if unchangedCurrent.SpecVersion != unchangedDeployment.SpecVersion {
		t.Fatalf("unrelated deployment version = %d, want %d", unchangedCurrent.SpecVersion, unchangedDeployment.SpecVersion)
	}
	if got := len(erru.Must(store.Queries().ListDeploymentEvents(context.Background(), int64(firstDeployment.DeploymentID)))); got != 2 {
		t.Fatalf("first deployment history length = %d, want 2", got)
	}

	_, _, err = AppendConfigVersion(store, database.ConfigID, "must-not-save", 9, true, []storage.DeploymentSpecVersion{
		{ID: firstDeployment.DeploymentID, SpecVersion: firstDeployment.SpecVersion},
		{ID: secondDeployment.DeploymentID, SpecVersion: secondCurrent.SpecVersion},
	})
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("stale update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	latest, ok := GetConfig(store.Queries(), database.ConfigID)
	if !ok || latestConfigRef(t, latest).ID != savedRef.ID || latestConfigRef(t, latest).Version != 3 {
		t.Fatalf("latest config after rollback = %+v, ok=%v", latest, ok)
	}
	if erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(firstDeployment.DeploymentID))).SpecVersion != firstCurrent.SpecVersion {
		t.Fatal("stale request changed deployment config")
	}
}

func TestRenameConfigPublishesEventAndPreservesHistory(t *testing.T) {
	store := openTestStore(t)
	meta := setConfigByName(store, "old-name", "one", 1)
	meta = setConfigByName(store, "old-name", "two", 1)
	metaSub, unsubscribe := store.SubscribeUpdates()
	defer unsubscribe()

	if _, err := RenameConfig(store, meta.ConfigID, "new-name"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	select {
	case tx := <-metaSub:
		if len(tx.ConfigEvents) != 1 {
			t.Fatalf("expected one event: %+v", tx)
		}
		update := tx.ConfigEvents[0]
		if update.Value.Fs.Name != "new-name" || update.ConfigID != meta.ConfigID {
			t.Fatalf("config update = %+v", update)
		}
		versions := statetest.ValueVersions(store, update)
		if len(versions) != 2 || versions[0].Value != "two" || versions[1].Value != "one" {
			t.Fatalf("config update value versions = %+v", versions)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config meta update")
	}
}

func TestConfigSoftDeleteHidesRowAndFreesName(t *testing.T) {
	store := openTestStore(t)
	cfg, err := CreateConfig(store, "mode", nodes.DefaultSpaceID, 0, 1, "on")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if c, err := DeleteConfig(store, cfg.ConfigID, nil); err != nil || c.EventType != apigen.EventType_EVENT_TYPE_DELETE {
		t.Fatalf("delete config = %+v err=%v", c, err)
	}
	if _, ok := GetConfig(store.Queries(), cfg.ConfigID); ok {
		t.Fatal("deleted config still resolves by id")
	}
	if got := ListConfigs(store.Queries()); len(got) != 0 {
		t.Fatalf("ListConfigs after delete = %d items, want 0", len(got))
	}
	recreated, err := CreateConfig(store, "mode", nodes.DefaultSpaceID, 0, 1, "off")
	if err != nil {
		t.Fatalf("recreate config with freed name: %v", err)
	}
	if recreated.ConfigID == cfg.ConfigID {
		t.Fatal("recreated config reused the deleted identity")
	}
	if ref, ok := GetConfigVersion(store.Queries(), statetest.ValueVersions(store, cfg)[0].ID); !ok || ref.Value != "on" {
		t.Fatalf("pinned version of deleted config = %+v ok=%v", ref, ok)
	}
	if _, err := DeleteConfig(store, cfg.ConfigID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a deleted config err = %v, want ErrNotFound", err)
	}
}
