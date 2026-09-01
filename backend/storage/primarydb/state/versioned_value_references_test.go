package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
)

func envRefSpec(configIDs map[string]int32, secretIDs map[string]int32) *apigen.DeploymentSpec {
	spec := testSpecWithState("v1", true)
	spec.Container1Spec.Runtime.EnvVars = make(map[string]*apigen.EnvVarValue, len(configIDs)+len(secretIDs))
	for key, id := range configIDs {
		id := id
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{ConfigVersionID: &id}
	}
	for key, id := range secretIDs {
		id := id
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{SecretVersionID: &id}
	}
	return spec
}

func deploymentEnvRefID(t *testing.T, cfg *apigen.Deployment, key string, secret bool) int32 {
	t.Helper()
	value := cfg.Spec.Container1Spec.Runtime.EnvVars[key]
	if value == nil {
		t.Fatalf("deployment %d env %s is missing", cfg.ID, key)
	}
	if secret {
		if value.SecretVersionID == nil {
			t.Fatalf("deployment %d env %s has no secret ref", cfg.ID, key)
		}
		return *value.SecretVersionID
	}
	if value.ConfigVersionID == nil {
		t.Fatalf("deployment %d env %s has no config ref", cfg.ID, key)
	}
	return *value.ConfigVersionID
}

// latestConfigRef returns the newest value version (value_versions[0]).
func latestConfigRef(t *testing.T, c *apigen.Config) *apigen.ConfigValueVersion {
	t.Helper()
	if c == nil || len(c.ValueVersions) == 0 {
		t.Fatalf("config has no value versions: %+v", c)
	}
	return c.ValueVersions[0]
}

// testSealFunc fabricates sealed bytes without a real SMK: reference-update
// mechanics do not care about the crypto.
func testSealFunc(value byte) secrets.SealFunc {
	return func(secretID, version int32) (secrets.SealedValue, error) {
		return secrets.SealedValue{SMKVersion: 1, Ciphertext: []byte{value}, Nonce: []byte{value}}, nil
	}
}

func TestSetUserConfigAtomicallyUpdatesReferencingDeployments(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")

	database := setConfigByName(store, "database", "one", 1)
	database = setConfigByName(store, "database", "two", 1)
	firstID := database.ValueVersions[1].ID
	secondID := database.ValueVersions[0].ID
	unrelated := setConfigByName(store, "other", "keep", 1)
	unrelatedID := latestConfigRef(t, unrelated).ID
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.Deployment {
		return store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, name, node.ID, spec)
	}
	firstDeployment := create("first", envRefSpec(map[string]int32{
		"DATABASE": firstID,
		"OTHER":    unrelatedID,
	}, nil))
	secondDeployment := create("second", envRefSpec(map[string]int32{"DATABASE": secondID}, nil))
	unchangedDeployment := create("unchanged", envRefSpec(map[string]int32{"OTHER": unrelatedID}, nil))

	saved, updatedIDs, err := store.AppendConfigVersionWithDeploymentUpdates(
		database.ID,
		"three",
		9,
		true,
		[]storage.DeploymentSpecVersion{
			{ID: firstDeployment.ID, SpecVersion: firstDeployment.SpecVersion},
			{ID: secondDeployment.ID, SpecVersion: secondDeployment.SpecVersion},
		},
	)
	if err != nil {
		t.Fatalf("set config with deployment updates: %v", err)
	}
	savedRef := latestConfigRef(t, saved)
	if savedRef.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("saved config = %+v, updated deployments = %v", saved, updatedIDs)
	}
	firstCurrent := store.deploymentCache[firstDeployment.ID]
	secondCurrent := store.deploymentCache[secondDeployment.ID]
	unchangedCurrent := store.deploymentCache[unchangedDeployment.ID]
	if got := deploymentEnvRefID(t, firstCurrent, "DATABASE", false); got != savedRef.ID {
		t.Fatalf("first deployment config ref = %d, want %d", got, savedRef.ID)
	}
	if got := deploymentEnvRefID(t, secondCurrent, "DATABASE", false); got != savedRef.ID {
		t.Fatalf("second deployment config ref = %d, want %d", got, savedRef.ID)
	}
	if got := deploymentEnvRefID(t, firstCurrent, "OTHER", false); got != unrelatedID {
		t.Fatalf("unrelated config ref = %d, want %d", got, unrelatedID)
	}
	if firstCurrent.SpecVersion != firstDeployment.SpecVersion+1 || secondCurrent.SpecVersion != secondDeployment.SpecVersion+1 {
		t.Fatalf("updated deployment versions = %d, %d", firstCurrent.SpecVersion, secondCurrent.SpecVersion)
	}
	if unchangedCurrent.SpecVersion != unchangedDeployment.SpecVersion {
		t.Fatalf("unrelated deployment version = %d, want %d", unchangedCurrent.SpecVersion, unchangedDeployment.SpecVersion)
	}
	if got := len(store.MustFetchDeploymentHistory(firstDeployment.ID)); got != 2 {
		t.Fatalf("first deployment history length = %d, want 2", got)
	}

	_, _, err = store.AppendConfigVersionWithDeploymentUpdates(
		database.ID,
		"must-not-save",
		9,
		true,
		[]storage.DeploymentSpecVersion{
			{ID: firstDeployment.ID, SpecVersion: firstDeployment.SpecVersion},
			{ID: secondDeployment.ID, SpecVersion: secondCurrent.SpecVersion},
		},
	)
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("stale update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	latest, ok := store.GetConfig(database.ID)
	if !ok || latestConfigRef(t, latest).ID != savedRef.ID || latestConfigRef(t, latest).Version != 3 {
		t.Fatalf("latest config after rollback = %+v, ok=%v", latest, ok)
	}
	if store.deploymentCache[firstDeployment.ID].SpecVersion != firstCurrent.SpecVersion {
		t.Fatal("stale request changed deployment config")
	}
}

func TestInsertSecretAtomicallyUpdatesAllHistoricalReferences(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")

	first, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.AppendSecretVersionWithDeploymentUpdates(first.SecretID, 0, testSealFunc(2), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, secretVersionID int32) *apigen.Deployment {
		return store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, name, node.ID, envRefSpec(nil, map[string]int32{"TOKEN": secretVersionID}))
	}
	firstDeployment := create("first", first.ID)
	secondDeployment := create("second", second.ID)

	third, updatedIDs, err := store.AppendSecretVersionWithDeploymentUpdates(first.SecretID, 0, testSealFunc(3), true, []storage.DeploymentSpecVersion{
		{ID: firstDeployment.ID, SpecVersion: firstDeployment.SpecVersion},
		{ID: secondDeployment.ID, SpecVersion: secondDeployment.SpecVersion},
	}, nil)
	if err != nil {
		t.Fatalf("insert secret with deployment updates: %v", err)
	}
	if third.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("secret = %+v, updated deployments = %v", third, updatedIDs)
	}
	if got := deploymentEnvRefID(t, store.deploymentCache[firstDeployment.ID], "TOKEN", true); got != third.ID {
		t.Fatalf("first deployment secret ref = %d, want %d", got, third.ID)
	}
	if got := deploymentEnvRefID(t, store.deploymentCache[secondDeployment.ID], "TOKEN", true); got != third.ID {
		t.Fatalf("second deployment secret ref = %d, want %d", got, third.ID)
	}

	_, _, err = store.AppendSecretVersionWithDeploymentUpdates(first.SecretID, 0, testSealFunc(4), true, []storage.DeploymentSpecVersion{{
		ID: firstDeployment.ID, SpecVersion: store.deploymentCache[firstDeployment.ID].SpecVersion,
	}}, nil)
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("incomplete update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	rows := store.ListSecretVersionRecords()
	if len(rows) != 3 {
		t.Fatalf("secret rows after rollback = %d, want 3", len(rows))
	}
}

func TestRenameConfigPublishesFullVersionIndex(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	meta := setConfigByName(store, "old-name", "one", 1)
	meta = setConfigByName(store, "old-name", "two", 1)
	metaSub, unsubscribe := store.SubscribeConfigUpdates()
	defer unsubscribe()

	renamed, err := store.RenameConfig(meta.ID, "new-name")
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	store.NotifyConfigUpdate(renamed)
	select {
	case update := <-metaSub.Ch:
		if update.Fs.Name != "new-name" || update.ID != meta.ID {
			t.Fatalf("config update = %+v", update)
		}
		// The full version index rides along, so subscribers keep every
		// version row id -> value mapping without a second channel.
		if len(update.ValueVersions) != 2 || update.ValueVersions[0].Value != "two" || update.ValueVersions[1].Value != "one" {
			t.Fatalf("config update value versions = %+v", update.ValueVersions)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config meta update")
	}
}

func TestRotationIgnoresDeletedDeploymentReferences(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")

	first, err := store.CreateSecretWithVersion("pgpassword", DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string) *apigen.Deployment {
		return store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, name, node.ID, envRefSpec(nil, map[string]int32{"POSTGRES_PASSWORD": first.ID}))
	}
	original := create("original")
	live := create("live")

	_, _, ok := store.UpdateDeploymentSpec(apigen.Context{}, original.ID, DeploymentSpecUpdate{
		ExpectedSpecVersion: original.SpecVersion + 1,
		Spec:                &original.Spec,
		Deleted:             true,
	})
	if !ok {
		t.Fatal("soft delete failed")
	}

	// The UI sends only the live deployment: the frontend filters tombstones out
	// when assembling referencingDeployments.
	second, updatedIDs, err := store.AppendSecretVersionWithDeploymentUpdates(first.SecretID, 0, testSealFunc(2), true, []storage.DeploymentSpecVersion{
		{ID: live.ID, SpecVersion: live.SpecVersion},
	}, nil)
	if err != nil {
		t.Fatalf("rotation rejected: %v", err)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != live.ID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, live.ID)
	}
	if got := deploymentEnvRefID(t, store.deploymentCache[live.ID], "POSTGRES_PASSWORD", true); got != second.ID {
		t.Fatalf("live deployment secret ref = %d, want %d", got, second.ID)
	}
	tombstone := store.deploymentCache[original.ID]
	if got := deploymentEnvRefID(t, tombstone, "POSTGRES_PASSWORD", true); got != first.ID {
		t.Fatalf("tombstone secret ref = %d, want it left at %d", got, first.ID)
	}
	if tombstone.SpecVersion != original.SpecVersion+1 {
		t.Fatalf("tombstone version = %d, want %d: rotation must not rewrite it",
			tombstone.SpecVersion, original.SpecVersion+1)
	}
}
