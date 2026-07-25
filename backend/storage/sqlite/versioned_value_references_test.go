package sqlite

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
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{ConfigID: &id}
	}
	for key, id := range secretIDs {
		id := id
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{SecretID: &id}
	}
	return spec
}

func deploymentEnvRefID(t *testing.T, cfg *apigen.DeploymentConfig, key string, secret bool) int32 {
	t.Helper()
	value := cfg.Spec.Container1Spec.Runtime.EnvVars[key]
	if value == nil {
		t.Fatalf("deployment %d env %s is missing", cfg.ID, key)
	}
	if secret {
		if value.SecretID == nil {
			t.Fatalf("deployment %d env %s has no secret ref", cfg.ID, key)
		}
		return *value.SecretID
	}
	if value.ConfigID == nil {
		t.Fatalf("deployment %d env %s has no config ref", cfg.ID, key)
	}
	return *value.ConfigID
}

func TestSetUserConfigAtomicallyUpdatesReferencingDeployments(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")

	first := store.SetUserConfig("database", "one", 1, DefaultSpaceID)
	second := store.SetUserConfig("database", "two", 1, DefaultSpaceID)
	unrelated := store.SetUserConfig("other", "keep", 1, DefaultSpaceID)
	create := func(name string, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
		return store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
			SpaceID: DefaultSpaceID,
			Name:    name,
		}, node.ID, spec)
	}
	firstDeployment := create("first", envRefSpec(map[string]int32{
		"DATABASE": first.ID,
		"OTHER":    unrelated.ID,
	}, nil))
	secondDeployment := create("second", envRefSpec(map[string]int32{"DATABASE": second.ID}, nil))
	unchangedDeployment := create("unchanged", envRefSpec(map[string]int32{"OTHER": unrelated.ID}, nil))

	saved, updatedIDs, err := store.SetUserConfigWithDeploymentUpdates(
		"database",
		"three",
		9,
		DefaultSpaceID,
		true,
		[]storage.DeploymentConfigVersion{
			{ID: firstDeployment.ID, Version: firstDeployment.Version},
			{ID: secondDeployment.ID, Version: secondDeployment.Version},
		},
	)
	if err != nil {
		t.Fatalf("set config with deployment updates: %v", err)
	}
	if saved.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("saved config = %+v, updated deployments = %v", saved, updatedIDs)
	}
	firstCurrent := store.configCache[firstDeployment.ID]
	secondCurrent := store.configCache[secondDeployment.ID]
	unchangedCurrent := store.configCache[unchangedDeployment.ID]
	if got := deploymentEnvRefID(t, firstCurrent, "DATABASE", false); got != saved.ID {
		t.Fatalf("first deployment config ref = %d, want %d", got, saved.ID)
	}
	if got := deploymentEnvRefID(t, secondCurrent, "DATABASE", false); got != saved.ID {
		t.Fatalf("second deployment config ref = %d, want %d", got, saved.ID)
	}
	if got := deploymentEnvRefID(t, firstCurrent, "OTHER", false); got != unrelated.ID {
		t.Fatalf("unrelated config ref = %d, want %d", got, unrelated.ID)
	}
	if firstCurrent.Version != firstDeployment.Version+1 || secondCurrent.Version != secondDeployment.Version+1 {
		t.Fatalf("updated deployment versions = %d, %d", firstCurrent.Version, secondCurrent.Version)
	}
	if unchangedCurrent.Version != unchangedDeployment.Version {
		t.Fatalf("unrelated deployment version = %d, want %d", unchangedCurrent.Version, unchangedDeployment.Version)
	}
	if got := len(store.MustFetchDeploymentHistory(firstDeployment.ID)); got != 2 {
		t.Fatalf("first deployment history length = %d, want 2", got)
	}

	_, _, err = store.SetUserConfigWithDeploymentUpdates(
		"database",
		"must-not-save",
		9,
		DefaultSpaceID,
		true,
		[]storage.DeploymentConfigVersion{
			{ID: firstDeployment.ID, Version: firstDeployment.Version},
			{ID: secondDeployment.ID, Version: secondCurrent.Version},
		},
	)
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("stale update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	latest, ok := store.GetLatestUserConfig("database")
	if !ok || latest.ID != saved.ID || latest.Version != 3 {
		t.Fatalf("latest config after rollback = %+v, ok=%v", latest, ok)
	}
	if store.configCache[firstDeployment.ID].Version != firstCurrent.Version {
		t.Fatal("stale request changed deployment config")
	}
}

func TestInsertSecretAtomicallyUpdatesAllHistoricalReferences(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")
	insert := func(value byte, update bool, expected []storage.DeploymentConfigVersion) (secrets.Record, []int32, error) {
		return store.InsertSecretWithDeploymentUpdates(secrets.Record{
			Name:       "token",
			SpaceID:    DefaultSpaceID,
			SMKVersion: 1,
			Ciphertext: []byte{value},
			Nonce:      []byte{value},
			CreatedAt:  int64(value),
		}, update, expected, nil)
	}
	first, _, err := insert(1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := insert(2, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, secretID int32) *apigen.DeploymentConfig {
		return store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
			SpaceID: DefaultSpaceID,
			Name:    name,
		}, node.ID, envRefSpec(nil, map[string]int32{"TOKEN": secretID}))
	}
	firstDeployment := create("first", first.ID)
	secondDeployment := create("second", second.ID)

	third, updatedIDs, err := insert(3, true, []storage.DeploymentConfigVersion{
		{ID: firstDeployment.ID, Version: firstDeployment.Version},
		{ID: secondDeployment.ID, Version: secondDeployment.Version},
	})
	if err != nil {
		t.Fatalf("insert secret with deployment updates: %v", err)
	}
	if third.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("secret = %+v, updated deployments = %v", third, updatedIDs)
	}
	if got := deploymentEnvRefID(t, store.configCache[firstDeployment.ID], "TOKEN", true); got != third.ID {
		t.Fatalf("first deployment secret ref = %d, want %d", got, third.ID)
	}
	if got := deploymentEnvRefID(t, store.configCache[secondDeployment.ID], "TOKEN", true); got != third.ID {
		t.Fatalf("second deployment secret ref = %d, want %d", got, third.ID)
	}

	_, _, err = insert(4, true, []storage.DeploymentConfigVersion{{
		ID: firstDeployment.ID, Version: store.configCache[firstDeployment.ID].Version,
	}})
	if !errors.Is(err, ErrReferencingDeploymentsChanged) {
		t.Fatalf("incomplete update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	rows := store.ListSecrets()
	if len(rows) != 3 {
		t.Fatalf("secret rows after rollback = %d, want 3", len(rows))
	}
}

func TestRenameUserConfigPublishesEveryHistoricalVersion(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	first := store.SetUserConfig("old-name", "one", 1, DefaultSpaceID)
	second := store.SetUserConfig("old-name", "two", 1, DefaultSpaceID)
	referenceSub, unsubscribeReferences := store.SubscribeUserConfigReferenceUpdates()
	defer unsubscribeReferences()
	valueSub, unsubscribeValues := store.SubscribeUserConfigValueUpdates()
	defer unsubscribeValues()

	if _, ok := store.RenameUserConfig("old-name", "new-name"); !ok {
		t.Fatal("rename failed")
	}
	wantIDs := map[int32]struct{}{first.ID: {}, second.ID: {}}
	for i := 0; i < 2; i++ {
		select {
		case ref := <-referenceSub.Ch:
			if ref.Name != "new-name" {
				t.Fatalf("reference name = %q", ref.Name)
			}
			delete(wantIDs, ref.ID)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for config reference update")
		}
		select {
		case value := <-valueSub.Ch:
			if value.Name != "new-name" {
				t.Fatalf("value name = %q", value.Name)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for config value update")
		}
	}
	if len(wantIDs) != 0 {
		t.Fatalf("missing renamed config IDs: %v", wantIDs)
	}
}

func TestRotationIgnoresDeletedDeploymentReferences(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary")
	insert := func(value byte, update bool, expected []storage.DeploymentConfigVersion) (secrets.Record, []int32, error) {
		return store.InsertSecretWithDeploymentUpdates(secrets.Record{
			Name:       "pgpassword",
			SpaceID:    DefaultSpaceID,
			SMKVersion: 1,
			Ciphertext: []byte{value},
			Nonce:      []byte{value},
			CreatedAt:  int64(value),
		}, update, expected, nil)
	}
	first, _, err := insert(1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string) *apigen.DeploymentConfig {
		return store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
			SpaceID: DefaultSpaceID,
			Name:    name,
		}, node.ID, envRefSpec(nil, map[string]int32{"POSTGRES_PASSWORD": first.ID}))
	}
	original := create("original")
	live := create("live")

	deleted := true
	_, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, original.ID, DeploymentConfigUpdate{
		ExpectedVersion: original.Version + 1,
		Deleted:         &deleted,
	})
	if !ok {
		t.Fatal("soft delete failed")
	}

	// The UI sends only the live deployment: the frontend filters tombstones out
	// when assembling referencingDeployments.
	second, updatedIDs, err := insert(2, true, []storage.DeploymentConfigVersion{
		{ID: live.ID, Version: live.Version},
	})
	if err != nil {
		t.Fatalf("rotation rejected: %v", err)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != live.ID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, live.ID)
	}
	if got := deploymentEnvRefID(t, store.configCache[live.ID], "POSTGRES_PASSWORD", true); got != second.ID {
		t.Fatalf("live deployment secret ref = %d, want %d", got, second.ID)
	}
	tombstone := store.configCache[original.ID]
	if got := deploymentEnvRefID(t, tombstone, "POSTGRES_PASSWORD", true); got != first.ID {
		t.Fatalf("tombstone secret ref = %d, want it left at %d", got, first.ID)
	}
	if tombstone.Version != original.Version+1 {
		t.Fatalf("tombstone version = %d, want %d: rotation must not rewrite it",
			tombstone.Version, original.Version+1)
	}
}
