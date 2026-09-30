package secrets

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
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
	return func(int32) (SealedValue, error) {
		return SealedValue{SMKVersion: 1, Ciphertext: []byte{value}, Nonce: []byte{value}}, nil
	}
}

func expectedSeqs(events ...*apigen.DeploymentEvent) []*apigen.DeploymentExpectedSeq {
	var out []*apigen.DeploymentExpectedSeq
	for _, e := range events {
		out = append(out, &apigen.DeploymentExpectedSeq{DeploymentID: e.DeploymentID, ExpectedSeq: e.Seq})
	}
	return out
}

func mutationsOf(update state.WriteUpdate, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for _, m := range update.Mutations {
		if m.Type() == typ {
			out = append(out, m)
		}
	}
	return out
}

func latestDeploymentEvent(t *testing.T, store *state.Service, id int32) *apigen.DeploymentEvent {
	t.Helper()
	return erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(id)))
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

	third, updatedIDs, err := appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(3), true, expectedSeqs(firstDeployment, secondDeployment), nil)
	if err != nil {
		t.Fatalf("insert secret with deployment updates: %v", err)
	}
	if third.Version != 3 || len(updatedIDs) != 2 {
		t.Fatalf("secret = %+v, updated deployments = %v", third, updatedIDs)
	}
	if got := statetest.DeploymentEnvRef(t, latestDeploymentEvent(t, store, firstDeployment.DeploymentID), "TOKEN", true); got != third.ref() {
		t.Fatalf("first deployment secret ref = %v, want %v", got, third.ref())
	}
	if got := statetest.DeploymentEnvRef(t, latestDeploymentEvent(t, store, secondDeployment.DeploymentID), "TOKEN", true); got != third.ref() {
		t.Fatalf("second deployment secret ref = %v, want %v", got, third.ref())
	}

	_, _, err = appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(4), true, expectedSeqs(latestDeploymentEvent(t, store, firstDeployment.DeploymentID)), nil)
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

	second, updatedIDs, err := appendVersionWithDeploymentUpdates(store, first.SecretID, 0, testSealFunc(2), true, expectedSeqs(live), nil)
	if err != nil {
		t.Fatalf("rotation rejected: %v", err)
	}
	if len(updatedIDs) != 1 || updatedIDs[0] != live.DeploymentID {
		t.Fatalf("updated deployments = %v, want only %d", updatedIDs, live.DeploymentID)
	}
	if got := statetest.DeploymentEnvRef(t, latestDeploymentEvent(t, store, live.DeploymentID), "POSTGRES_PASSWORD", true); got != second.ref() {
		t.Fatalf("live deployment secret ref = %v, want %v", got, second.ref())
	}
	tombstone := latestDeploymentEvent(t, store, original.DeploymentID)
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

func TestRotationChecksExpectedSeqsBeforeSealingAndPreservesRenames(t *testing.T) {
	store := openTestStore(t)
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secret, err := CreateWithVersion(store, "token", nodes.DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatal(err)
	}
	first := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "first", node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"TOKEN": secret.ref()}))
	second := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "second", node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"TOKEN": secret.ref()}))
	renamed := statetest.RenameDeployment(store, apigen.Context{}, second.DeploymentID, "renamed")
	if renamed.Seq <= second.Seq {
		t.Fatal("test requires the rename to advance the deployment's seq")
	}
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	sealed := false
	seal := func(id int32) (SealedValue, error) {
		sealed = true
		return testSealFunc(2)(id)
	}
	expected := expectedSeqs(first, second)
	ctx := context.Background()
	seqBefore := erru.Must(store.Queries().GetGlobalSeq(ctx))
	before := statetest.Bootstrap(t, store.Queries())
	_, _, err = appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, seal, true, expected, nil)
	if !errors.Is(err, values.ErrReferencingDeploymentsChanged) || sealed {
		t.Fatalf("stale rotation: err=%v, sealed=%v", err, sealed)
	}
	if erru.Must(store.Queries().GetGlobalSeq(ctx)) != seqBefore || !reflect.DeepEqual(before, statetest.Bootstrap(t, store.Queries())) || len(ListVersionRecords(store.Queries())) != 1 {
		t.Fatal("stale rotation changed state, sequence or sealed rows")
	}
	select {
	case <-sub:
		t.Fatal("stale rotation published")
	default:
	}
	expected[1].ExpectedSeq = renamed.Seq
	rotated, ids, err := appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, seal, true, expected, nil)
	if err != nil || !sealed || len(ids) != 2 {
		t.Fatalf("valid rotation: err=%v, sealed=%v, ids=%v", err, sealed, ids)
	}
	current := latestDeploymentEvent(t, store, second.DeploymentID)
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
	var deployments []*apigen.DeploymentEvent
	for _, name := range []string{"one", "two"} {
		deployments = append(deployments, statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, name, node.ID, statetest.EnvRefSpec(nil, map[string]apigen.ValueRef{"PASSWORD": secret.ref()})))
	}
	seqBefore := erru.Must(store.Queries().GetGlobalSeq(context.Background()))
	sub, unsub := store.SubscribeUpdates()
	defer unsub()
	if _, _, err = appendVersionWithDeploymentUpdates(store, secret.SecretID, 1, testSealFunc(2), true, expectedSeqs(deployments...), nil); err != nil {
		t.Fatal(err)
	}
	update := <-sub
	statetest.AssertUpdateMatchesRows(t, store, update)
	secretMutations := mutationsOf(update, apigen.CoreEntityType_CORE_ENTITY_SECRET)
	deploymentMutations := mutationsOf(update, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
	if update.Seq != seqBefore+1 || len(update.Mutations) != 3 || len(secretMutations) != 1 || len(deploymentMutations) != 2 {
		t.Fatalf("incomplete transaction: %+v", update)
	}
	if len(secretMutations[0].Entity().Secret.Ciphertext) == 0 || secretMutations[0].Entity().Secret.SmkVersion == 0 {
		t.Fatalf("secret mutation = %+v, want the sealed value on the payload", secretMutations[0].Entity().Secret)
	}
	pin := apigen.ValueRef{ID: int32(secretMutations[0].EntityID()), Version: secretMutations[0].Entity().Secret.ValueVersion}
	if pin != (apigen.ValueRef{ID: secret.SecretID, Version: 2}) {
		t.Fatalf("published pin = %v, want version 2 of secret %d", pin, secret.SecretID)
	}
	for _, m := range deploymentMutations {
		d := latestDeploymentEvent(t, store, int32(m.EntityID()))
		published := &apigen.DeploymentEvent{DeploymentID: d.DeploymentID, Value: *m.Entity().Deployment}
		if d.Seq != update.Seq || m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_UPDATE || statetest.DeploymentEnvRef(t, published, "PASSWORD", true) != pin || statetest.DeploymentEnvRef(t, d, "PASSWORD", true) != pin {
			t.Fatalf("deployment was not frozen with rotation: %+v", m)
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
