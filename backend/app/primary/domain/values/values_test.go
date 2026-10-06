package values

import (
	"context"
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func openTestStore(t *testing.T) *state.Service {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func setConfigByName(s *state.Service, name, value string, author int64) *pq.ConfigEvent {
	for _, cfg := range ListConfigs(s.Queries()) {
		if cfg.Value.Fs.Key != name || cfg.SpaceID() != nodes.DefaultSpaceID || cfg.Value.Fs.DirectoryID.Present {
			continue
		}
		event, _, err := AppendConfigVersion(s, cfg.ConfigID, value, author, false, nil)
		return erru.Must(event, err)
	}
	return erru.Must(CreateConfig(s, name, nodes.DefaultSpaceID, 0, author, value))
}

func expectedSeqs(records ...*apigen.DeploymentRecord) []apigen.DeploymentExpectedSeq {
	var out []apigen.DeploymentExpectedSeq
	for _, r := range records {
		out = append(out, apigen.DeploymentExpectedSeq{DeploymentID: r.Deployment.ID, ExpectedSeq: r.Meta.UpdatedSeq})
	}
	return out
}

func mutationsOf(update state.WriteUpdate, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for i := range update.Mutations {
		if m := &update.Mutations[i]; m.Type() == typ {
			out = append(out, m)
		}
	}
	return out
}

func latestConfigRef(t *testing.T, c *pq.ConfigEvent) *statetest.ValueVersion {
	t.Helper()
	if c == nil || c.ConfigID == 0 || c.Seq == 0 {
		t.Fatalf("config event missing: %+v", c)
	}
	return statetest.LatestValue(nil, c)
}

func TestSetUserConfigAtomicallyUpdatesReferencingDeployments(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))

	database := setConfigByName(store, "database", "one", 1)
	database = setConfigByName(store, "database", "two", 1)
	firstRef := statetest.ValueVersions(store, database)[1].Ref
	secondRef := statetest.ValueVersions(store, database)[0].Ref
	unrelated := setConfigByName(store, "other", "keep", 1)
	unrelatedRef := latestConfigRef(t, unrelated).Ref
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
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
	firstCurrent := erru.Must(store.Queries().GetLatestDeployment(context.Background(), firstDeployment.Deployment.ID))
	secondCurrent := erru.Must(store.Queries().GetLatestDeployment(context.Background(), secondDeployment.Deployment.ID))
	unchangedCurrent := erru.Must(store.Queries().GetLatestDeployment(context.Background(), unchangedDeployment.Deployment.ID))
	if got := statetest.DeploymentEnvRef(t, firstCurrent, "DATABASE", false); got != savedRef.Ref {
		t.Fatalf("first deployment config ref = %v, want %v", got, savedRef.Ref)
	}
	if got := statetest.DeploymentEnvRef(t, secondCurrent, "DATABASE", false); got != savedRef.Ref {
		t.Fatalf("second deployment config ref = %v, want %v", got, savedRef.Ref)
	}
	if got := statetest.DeploymentEnvRef(t, firstCurrent, "OTHER", false); got != unrelatedRef {
		t.Fatalf("unrelated config ref = %v, want %v", got, unrelatedRef)
	}
	if firstCurrent.Meta.SpecVersion != firstDeployment.Meta.SpecVersion+1 || secondCurrent.Meta.SpecVersion != secondDeployment.Meta.SpecVersion+1 {
		t.Fatalf("updated deployment versions = %d, %d", firstCurrent.Meta.SpecVersion, secondCurrent.Meta.SpecVersion)
	}
	if unchangedCurrent.Meta.SpecVersion != unchangedDeployment.Meta.SpecVersion {
		t.Fatalf("unrelated deployment version = %d, want %d", unchangedCurrent.Meta.SpecVersion, unchangedDeployment.Meta.SpecVersion)
	}
	if got := len(erru.Must(store.Queries().ListDeploymentHistory(context.Background(), firstDeployment.Deployment.ID))); got != 2 {
		t.Fatalf("first deployment history length = %d, want 2", got)
	}

	_, _, err = AppendConfigVersion(store, database.ConfigID, "must-not-save", 9, true, expectedSeqs(firstDeployment, secondCurrent))
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("stale update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	latest, ok := GetConfig(store.Queries(), database.ConfigID)
	if !ok || latestConfigRef(t, latest).Ref != savedRef.Ref || latestConfigRef(t, latest).Version != 3 {
		t.Fatalf("latest config after rollback = %+v, ok=%v", latest, ok)
	}
	if erru.Must(store.Queries().GetLatestDeployment(context.Background(), firstDeployment.Deployment.ID)).Meta.SpecVersion != firstCurrent.Meta.SpecVersion {
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
		update := configs[0].Entity().Value.Config
		if update.Fs.Key != "new-name" || configs[0].EntityID() != meta.ConfigID || configs[0].Meta().ValueVersion != 2 || update.Value != "two" {
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
	if c, err := DeleteConfig(store, cfg.ConfigID, nil); err != nil || c.ConfigID != cfg.ConfigID {
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
	if ref, ok := GetConfigVersion(store.Queries(), apigen.ValueRef{ID: cfg.ConfigID, Version: cfg.ValueVersion}); ok {
		t.Fatalf("version of deleted config still resolves: %+v", ref)
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
	if same.Seq != created.Seq || same.ValueVersion != created.ValueVersion || len(updatedIDs) != 0 {
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	database := setConfigByName(store, "database", "one", 1)
	database = setConfigByName(store, "database", "two", 1)
	oldRef := statetest.ValueVersions(store, database)[1].Ref
	currentRef := statetest.ValueVersions(store, database)[0].Ref
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, spec)
	}
	stale := create("stale", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": oldRef}, nil))
	current := create("current", statetest.EnvRefSpec(map[string]apigen.ValueRef{"DATABASE": currentRef}, nil))

	saved, updatedIDs, err := AppendConfigVersion(store, database.ConfigID, "two", 9, true, expectedSeqs(stale, current))
	if err != nil {
		t.Fatalf("no-op set with deployment updates: %v", err)
	}
	if saved.ConfigID != database.ConfigID || saved.ValueVersion != 2 {
		t.Fatalf("saved = %+v, want the current version 2 event", saved)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != stale.Deployment.ID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, stale.Deployment.ID)
	}
	staleNow := erru.Must(store.Queries().GetLatestDeployment(context.Background(), stale.Deployment.ID))
	currentNow := erru.Must(store.Queries().GetLatestDeployment(context.Background(), current.Deployment.ID))
	if got := statetest.DeploymentEnvRef(t, staleNow, "DATABASE", false); got != currentRef || staleNow.Meta.SpecVersion != stale.Meta.SpecVersion+1 {
		t.Fatalf("stale deployment ref = %v specVersion %d, want %v and %d", got, staleNow.Meta.SpecVersion, currentRef, stale.Meta.SpecVersion+1)
	}
	if currentNow.Meta.Version != current.Meta.Version {
		t.Fatalf("deployment already at the current version was rewritten: %d -> %d", current.Meta.Version, currentNow.Meta.Version)
	}
	if got := len(statetest.ValueVersions(store, database)); got != 2 {
		t.Fatalf("config history length = %d, want 2", got)
	}
}
