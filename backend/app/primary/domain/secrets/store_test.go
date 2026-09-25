package secrets

import (
	"context"
	"errors"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
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

func testSealFunc(value byte) SealFunc {
	return func(secretID, version int32) (SealedValue, error) {
		return SealedValue{SMKVersion: 1, Ciphertext: []byte{value}, Nonce: []byte{value}}, nil
	}
}

func TestInsertSecretAtomicallyUpdatesAllHistoricalReferences(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	first, err := CreateWithVersion(store, "token", nodes.DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(2), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, secret apigen.ValueRef) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"TOKEN": secret}))
	}
	firstDeployment := create("first", first.ref())
	secondDeployment := create("second", second.ref())

	third, updatedIDs, err := appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(3), true, []storage.DeploymentSpecVersion{
		{ID: firstDeployment.DeploymentID, SpecVersion: firstDeployment.SpecVersion},
		{ID: secondDeployment.DeploymentID, SpecVersion: secondDeployment.SpecVersion},
	}, nil)
	if err != nil {
		t.Fatalf("insert secret with deployment updates: %v", err)
	}
	if third.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("secret = %+v, updated deployments = %v", third, updatedIDs)
	}
	if got := statetest.DeploymentEnvRef(t, erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(firstDeployment.DeploymentID))), "TOKEN", true); got != third.ref() {
		t.Fatalf("first deployment secret ref = %v, want %v", got, third.ref())
	}
	if got := statetest.DeploymentEnvRef(t, erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(secondDeployment.DeploymentID))), "TOKEN", true); got != third.ref() {
		t.Fatalf("second deployment secret ref = %v, want %v", got, third.ref())
	}

	_, _, err = appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(4), true, []storage.DeploymentSpecVersion{{
		ID: firstDeployment.DeploymentID, SpecVersion: erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(firstDeployment.DeploymentID))).SpecVersion,
	}}, nil)
	if !errors.Is(err, values.ErrReferencingDeploymentsChanged) {
		t.Fatalf("incomplete update error = %v, want ErrReferencingDeploymentsChanged", err)
	}
	if rows := ListVersionRecords(store.Queries()); len(rows) != 3 {
		t.Fatalf("secret rows after rollback = %d, want 3", len(rows))
	}
}

func TestRotationIgnoresDeletedDeploymentReferences(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	first, err := CreateWithVersion(store, "pgpassword", nodes.DefaultSpaceID, 0, 0, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string) *apigen.DeploymentEvent {
		return statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"POSTGRES_PASSWORD": first.ref()}))
	}
	original := create("original")
	live := create("live")
	statetest.DeleteDeployment(store, apigen.Context{}, original.DeploymentID)

	second, updatedIDs, err := appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(2), true, []storage.DeploymentSpecVersion{
		{ID: live.DeploymentID, SpecVersion: live.SpecVersion},
	}, nil)
	if err != nil {
		t.Fatalf("rotation rejected: %v", err)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != live.DeploymentID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, live.DeploymentID)
	}
	if got := statetest.DeploymentEnvRef(t, erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(live.DeploymentID))), "POSTGRES_PASSWORD", true); got != second.ref() {
		t.Fatalf("live deployment secret ref = %v, want %v", got, second.ref())
	}
	tombstone := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(original.DeploymentID)))
	if tombstone == nil || !tombstone.Deleted() {
		t.Fatal("deleted deployment still live")
	}
	if got := statetest.DeploymentEnvRef(t, tombstone, "POSTGRES_PASSWORD", true); got != first.ref() {
		t.Fatalf("tombstone secret ref = %v, want it left at %v", got, first.ref())
	}
	if tombstone.SpecVersion != original.SpecVersion || tombstone.Version != original.Version+1 {
		t.Fatalf("tombstone = v%d specV%d, want v%d specV%d", tombstone.Version, tombstone.SpecVersion, original.Version+1, original.SpecVersion)
	}
}

func TestRotationChecksSpecVersionsBeforeSealingAndPreservesRenames(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secret, err := CreateWithVersion(store, "token", nodes.DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	first := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "first", node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"TOKEN": secret.ref()}))
	second := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "second", node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"TOKEN": secret.ref()}))
	renamed := statetest.RenameDeployment(store, apigen.Context{}, second.DeploymentID, "renamed")
	if renamed.Version == renamed.SpecVersion || renamed.Seq == int64(renamed.SpecVersion) {
		t.Fatal("test requires distinct entity, facet and global counters")
	}
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	sealed := false
	seal := func(id, version int32) (SealedValue, error) {
		sealed = true
		return testSealFunc(2)(id, version)
	}
	expected := []storage.DeploymentSpecVersion{
		{ID: first.DeploymentID, SpecVersion: first.SpecVersion},
		{ID: second.DeploymentID, SpecVersion: renamed.Version},
	}
	before := store.BuildSnapshot(context.Background())
	_, _, err = appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, seal, true, expected, nil)
	if !errors.Is(err, values.ErrReferencingDeploymentsChanged) || sealed {
		t.Fatalf("stale rotation: err=%v, sealed=%v", err, sealed)
	}
	if !reflect.DeepEqual(before, store.BuildSnapshot(context.Background())) || len(ListVersionRecords(store.Queries())) != 1 {
		t.Fatal("stale rotation changed state, sequence or sealed rows")
	}
	select {
	case <-sub:
		t.Fatal("stale rotation published")
	default:
	}
	expected[1].SpecVersion = second.SpecVersion
	rotated, ids, err := appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, seal, true, expected, nil)
	if err != nil || !sealed || len(ids) != 2 {
		t.Fatalf("valid rotation: err=%v, sealed=%v, ids=%v", err, sealed, ids)
	}
	current := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(second.DeploymentID)))
	if current.Value.Name != "renamed" || current.Version != renamed.Version+1 || current.SpecVersion != second.SpecVersion+1 || statetest.DeploymentEnvRef(t, current, "TOKEN", true) != rotated.ref() {
		t.Fatalf("rotation lost the latest deployment state: %+v", current)
	}
	statetest.AssertUpdateMatchesRows(t, store, <-sub)
}

func TestTransactionUpdateIncludesRotationAndAllDeploymentEvents(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secret, err := CreateWithVersion(store, "password", nodes.DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	var expected []storage.DeploymentSpecVersion
	for _, name := range []string{"one", "two"} {
		d := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"PASSWORD": secret.ref()}))
		expected = append(expected, storage.DeploymentSpecVersion{ID: d.DeploymentID, SpecVersion: d.SpecVersion})
	}
	before := store.BuildSnapshot(context.Background())
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	if _, _, err = appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, testSealFunc(2), true, expected, nil); err != nil {
		t.Fatal(err)
	}
	update := <-sub
	statetest.AssertUpdateMatchesRows(t, store, update)
	if update.Seq != before.Seq+1 || len(update.SecretEvents) != 1 || len(update.DeploymentEvents) != 2 {
		t.Fatalf("incomplete transaction: %+v", update)
	}
	pin := apigen.ValueRef{ID: update.SecretEvents[0].SecretID, Version: update.SecretEvents[0].ValueVersion}
	for _, d := range update.DeploymentEvents {
		if d.Seq != update.Seq || statetest.DeploymentEnvRef(t, d, "PASSWORD", true) != pin {
			t.Fatalf("deployment was not frozen with rotation: %+v", d)
		}
	}
	select {
	case extra := <-sub:
		t.Fatalf("transaction published twice: %+v", extra)
	default:
	}
}

func TestSecretSoftDeleteHidesRowAndFreesName(t *testing.T) {
	store := openTestStore(t)
	sec, err := CreateWithVersion(store, "token", nodes.DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := deleteSecret(store, sec.SecretID, nil); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	if _, ok := IDByName(store.Queries(), nodes.DefaultSpaceID, "token"); ok {
		t.Fatal("deleted secret still resolves by name")
	}
	if got := ListVersionRecords(store.Queries()); len(got) != 0 {
		t.Fatalf("deleted secret leaked %d records into the manager load", len(got))
	}
	if _, err := CreateWithVersion(store, "token", nodes.DefaultSpaceID, 0, 1, testSealFunc(2)); err != nil {
		t.Fatalf("recreate secret with freed name: %v", err)
	}
}
