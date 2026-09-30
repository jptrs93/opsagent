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

func expectedSeqs(events ...*apigen.DeploymentEvent) []*apigen.DeploymentExpectedSeq {
	var out []*apigen.DeploymentExpectedSeq
	for _, e := range events {
		out = append(out, &apigen.DeploymentExpectedSeq{DeploymentID: e.DeploymentID, ExpectedSeq: e.Seq})
	}
	return out
}

func mutationsOf(update state.Update, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for _, m := range update.Mutations {
		if m.Type() == typ {
			out = append(out, m)
		}
	}
	return out
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
	firstRef := statetest.ValueVersions(store, database)[1].Ref
	secondRef := statetest.ValueVersions(store, database)[0].Ref
	unrelated := setConfigByName(store, "other", "keep", 1)
	unrelatedRef := latestConfigRef(t, unrelated).Ref
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, spec)
	}
	firstDeployment := create("first", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": firstRef, "OTHER": unrelatedRef}, nil))
	secondDeployment := create("second", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": secondRef}, nil))
	unchangedDeployment := create("unchanged", statetest.EnvRefSpec(map[string]apigen.ValueRef{"OTHER": unrelatedRef}, nil))

	saved, updatedIDs, err := AppendConfigVersion(store, database.ConfigID, "three", 9, true, expectedSeqs(firstDeployment, secondDeployment))
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
	if got := statetest.DeploymentEnvRef(t, firstCurrent, "DATABASE", false); got != savedRef.Ref {
		t.Fatalf("first deployment config ref = %v, want %v", got, savedRef.Ref)
	}
	if got := statetest.DeploymentEnvRef(t, secondCurrent, "DATABASE", false); got != savedRef.Ref {
		t.Fatalf("second deployment config ref = %v, want %v", got, savedRef.Ref)
	}
	if got := statetest.DeploymentEnvRef(t, firstCurrent, "OTHER", false); got != unrelatedRef {
		t.Fatalf("unrelated config ref = %v, want %v", got, unrelatedRef)
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

	_, _, err = AppendConfigVersion(store, database.ConfigID, "must-not-save", 9, true, expectedSeqs(firstDeployment, secondCurrent))
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
		statetest.AssertUpdateMatchesRows(t, store, tx)
		configs := mutationsOf(tx, apigen.CoreEntityType_CORE_ENTITY_CONFIG)
		if len(tx.Mutations) != 1 || len(configs) != 1 || configs[0].Kind() != apigen.AuthzVerb_AUTHZ_VERB_UPDATE {
			t.Fatalf("expected one config update: %+v", tx)
		}
		update := configs[0].Entity().Config
		if update.Fs.Name != "new-name" || int32(configs[0].EntityID()) != meta.ConfigID || update.ValueVersion != 2 || update.Value != "two" {
			t.Fatalf("config update = %+v", update)
		}
		renamed, ok := GetConfig(store.Queries(), meta.ConfigID)
		if !ok {
			t.Fatal("renamed config missing")
		}
		versions := statetest.ValueVersions(store, renamed)
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
	if ref, ok := GetConfigVersion(store.Queries(), statetest.ValueVersions(store, cfg)[0].Ref); !ok || ref.Value != "on" {
		t.Fatalf("pinned version of deleted config = %+v ok=%v", ref, ok)
	}
	if _, err := DeleteConfig(store, cfg.ConfigID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a deleted config err = %v, want ErrNotFound", err)
	}
}

func TestSetConfigSameValueIsNoOp(t *testing.T) {
	store := openTestStore(t)
	created := setConfigByName(store, "database", "one", 1)
	seqBefore := erru.Must(store.Queries().GetGlobalSeq(context.Background()))

	same, updatedIDs, err := AppendConfigVersion(store, created.ConfigID, "one", 2, false, nil)
	if err != nil {
		t.Fatalf("no-op set: %v", err)
	}
	if same.EventID != created.EventID || same.Version != created.Version || same.ValueVersion != created.ValueVersion || len(updatedIDs) != 0 {
		t.Fatalf("no-op set returned %+v (updated %v), want the current event %+v", same, updatedIDs, created)
	}
	if seqAfter := erru.Must(store.Queries().GetGlobalSeq(context.Background())); seqAfter != seqBefore {
		t.Fatalf("no-op set advanced the global seq from %d to %d", seqBefore, seqAfter)
	}
	if got := len(statetest.ValueVersions(store, created)); got != 1 {
		t.Fatalf("config history length = %d, want 1", got)
	}
}

func TestSetConfigSameValueStillRepointsStaleDeployments(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	database := setConfigByName(store, "database", "one", 1)
	database = setConfigByName(store, "database", "two", 1)
	oldRef := statetest.ValueVersions(store, database)[1].Ref
	currentRef := statetest.ValueVersions(store, database)[0].Ref
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, spec)
	}
	stale := create("stale", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": oldRef}, nil))
	current := create("current", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": currentRef}, nil))

	saved, updatedIDs, err := AppendConfigVersion(store, database.ConfigID, "two", 9, true, expectedSeqs(stale, current))
	if err != nil {
		t.Fatalf("no-op set with deployment updates: %v", err)
	}
	if saved.EventID != database.EventID || saved.ValueVersion != 2 {
		t.Fatalf("saved = %+v, want the current version 2 event", saved)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != stale.DeploymentID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, stale.DeploymentID)
	}
	staleNow := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(stale.DeploymentID)))
	currentNow := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(current.DeploymentID)))
	if got := statetest.DeploymentEnvRef(t, staleNow, "DATABASE", false); got != currentRef || staleNow.SpecVersion != stale.SpecVersion+1 {
		t.Fatalf("stale deployment ref = %v specVersion %d, want %v and %d", got, staleNow.SpecVersion, currentRef, stale.SpecVersion+1)
	}
	if currentNow.Version != current.Version {
		t.Fatalf("deployment already at the current version was rewritten: %d -> %d", current.Version, currentNow.Version)
	}
	if got := len(statetest.ValueVersions(store, database)); got != 2 {
		t.Fatalf("config history length = %d, want 2", got)
	}
}
