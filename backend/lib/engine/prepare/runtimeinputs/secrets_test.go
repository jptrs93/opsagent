package runtimeinputs

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeSecretProvider struct {
	ids    []int32
	values map[int32]string
}

type fakeConfigProvider struct {
	ids    []int32
	values map[int32]string
}

func (f *fakeSecretProvider) FetchSecrets(ctx context.Context, ids []int32) (map[int32]string, error) {
	f.ids = append([]int32(nil), ids...)
	if f.values != nil {
		return f.values, nil
	}
	values := make(map[int32]string, len(ids))
	for _, id := range ids {
		values[id] = "value"
	}
	return values, nil
}

func (f *fakeConfigProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
	f.ids = append([]int32(nil), ids...)
	if f.values != nil {
		return f.values, nil
	}
	values := make(map[int32]string, len(ids))
	for _, id := range ids {
		values[id] = "value"
	}
	return values, nil
}

func TestSecretRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
			"DB":    {SecretVersionID: ptrInt32(6)},
			"MIX":   {ConfigVersionID: ptrInt32(3)},
			"TOKEN": {SecretVersionID: ptrInt32(2)},
			"DUP":   {SecretVersionID: ptrInt32(6)},
		}},
	}}}

	want := []int32{2, 6}
	if got := SecretRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs() = %#v; want %#v", got, want)
	}
}

func TestConfigRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
			"URL":    {ConfigVersionID: ptrInt32(18)},
			"DUP":    {ConfigVersionID: ptrInt32(18)},
			"OTHER":  {ConfigVersionID: ptrInt32(2)},
			"SECRET": {SecretVersionID: ptrInt32(9)},
		}},
	}}}

	want := []int32{2, 18}
	if got := ConfigRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigRefs() = %#v; want %#v", got, want)
	}
}

func TestRequiredAssetRefsIncludesExplicitAndEnvAssets(t *testing.T) {
	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{
			AssetMounts: []*apigen.AssetMount{{AssetVersionID: 8, Permission: apigen.FilePermission_READ_EXECUTE}},
			EnvVars: map[string]*apigen.EnvVarValue{
				"APP_CONFIG": {Asset: "implicit.conf", AssetVersionID: 12},
				"PLAIN":      {Value: ptrString("value")},
			},
		},
	}}}

	refs := RequiredAssetRefs(dep)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d; want 2", len(refs))
	}
	if refs[0].AssetVersionID != 8 || refs[0].Label != "asset mount 8" || !refs[0].Executable {
		t.Fatalf("refs[0] = %+v", refs[0])
	}
	if refs[1].AssetVersionID != 12 || refs[1].Label != `asset env var "APP_CONFIG"` || refs[1].Executable {
		t.Fatalf("refs[1] = %+v", refs[1])
	}
}

func TestAssetCachePathWithModeUsesSeparateExecutablePath(t *testing.T) {
	readonly := AssetCachePathWithMode(8, false)
	executable := AssetCachePathWithMode(8, true)
	if readonly == executable {
		t.Fatalf("readonly and executable cache paths match: %q", readonly)
	}
	if AssetCacheMode(false) != 0o644 {
		t.Fatalf("readonly cache mode = %o", AssetCacheMode(false))
	}
	if AssetCacheMode(true) != 0o755 {
		t.Fatalf("executable cache mode = %o", AssetCacheMode(true))
	}
}

func TestEnsureSecretsReadyFetchesBatch(t *testing.T) {
	fake := &fakeSecretProvider{}
	inputs := New(nil, fake, nil)

	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
			"A": {SecretVersionID: ptrInt32(1)},
			"B": {SecretVersionID: ptrInt32(2)},
		}},
	}}}

	if err := inputs.EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	want := []int32{1, 2}
	if !reflect.DeepEqual(fake.ids, want) {
		t.Fatalf("fetched ids = %#v; want %#v", fake.ids, want)
	}
	if value, ok := inputs.ResolveSecret(2); !ok || value != "value" {
		t.Fatalf("ResolveSecret(2) = %q, %t; want value, true", value, ok)
	}
}

func TestEnsureConfigsReadyFetchesBatch(t *testing.T) {
	fake := &fakeConfigProvider{}
	inputs := New(nil, nil, fake)

	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
			"A": {ConfigVersionID: ptrInt32(1)},
			"B": {ConfigVersionID: ptrInt32(2)},
		}},
	}}}

	if err := inputs.EnsureConfigsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureConfigsReady: %v", err)
	}
	want := []int32{1, 2}
	if !reflect.DeepEqual(fake.ids, want) {
		t.Fatalf("fetched ids = %#v; want %#v", fake.ids, want)
	}
	if value, ok := inputs.ResolveConfig(2); !ok || value != "value" {
		t.Fatalf("ResolveConfig(2) = %q, %t; want value, true", value, ok)
	}
}

func TestEnsureSecretsReadyDoesNotCacheIncompleteBatch(t *testing.T) {
	fake := &fakeSecretProvider{values: map[int32]string{1: "one"}}
	inputs := New(nil, fake, nil)
	dep := &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{
			"A": {SecretVersionID: ptrInt32(1)},
			"B": {SecretVersionID: ptrInt32(2)},
		}},
	}}}

	if err := inputs.EnsureSecretsReady(context.Background(), dep); err == nil {
		t.Fatal("expected incomplete secret batch to fail")
	}
	if _, ok := inputs.ResolveSecret(1); ok {
		t.Fatal("incomplete secret batch was cached")
	}
}

// fakePersistence records what was written so tests can assert the durable side
// independently of the in-memory maps.
type fakePersistence struct {
	secrets map[int32]string
	configs map[int32]string
	loadErr error
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{secrets: map[int32]string{}, configs: map[int32]string{}}
}

func (f *fakePersistence) LoadRuntimeInputs() (map[int32]string, map[int32]string, error) {
	if f.loadErr != nil {
		return nil, nil, f.loadErr
	}
	secrets := map[int32]string{}
	configs := map[int32]string{}
	for id, v := range f.secrets {
		secrets[id] = v
	}
	for id, v := range f.configs {
		configs[id] = v
	}
	return secrets, configs, nil
}

func (f *fakePersistence) StoreRuntimeInputs(secrets, configs map[int32]string) error {
	for id, v := range secrets {
		f.secrets[id] = v
	}
	for id, v := range configs {
		f.configs[id] = v
	}
	return nil
}

func (f *fakePersistence) RetainRuntimeInputs(secrets, configs map[int32]struct{}) (int, error) {
	removed := 0
	for id := range f.secrets {
		if _, ok := secrets[id]; !ok {
			delete(f.secrets, id)
			removed++
		}
	}
	for id := range f.configs {
		if _, ok := configs[id]; !ok {
			delete(f.configs, id)
			removed++
		}
	}
	return removed, nil
}

func secretRefDeployment(ids ...int32) *apigen.Deployment {
	env := map[string]*apigen.EnvVarValue{}
	for i, id := range ids {
		env[string(rune('A'+i))] = &apigen.EnvVarValue{SecretVersionID: ptrInt32(id)}
	}
	return &apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: env},
	}}}
}

// The whole point of persisting runtime inputs: a restarted secondary resolves
// everything its workloads need without a single call to the primary, so it can
// cold-start while the primary is down.
func TestPersistedInputsMakeRestartNotContactTheProvider(t *testing.T) {
	persistence := newFakePersistence()
	fake := &fakeSecretProvider{}
	inputs, err := NewPersistent(nil, fake, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	dep := secretRefDeployment(1, 2)
	if err := inputs.EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if len(persistence.secrets) != 2 {
		t.Fatalf("persisted secrets = %v, want 2 entries", persistence.secrets)
	}

	// Restart: a fresh RuntimeInputs over the same durable store, with a provider
	// that fails every call the way an unreachable primary would.
	restarted, err := NewPersistent(nil, &failingSecretProvider{t: t}, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent after restart: %v", err)
	}
	if err := restarted.EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady after restart: %v", err)
	}
	if value, ok := restarted.ResolveSecret(2); !ok || value != "value" {
		t.Fatalf("ResolveSecret(2) after restart = %q, %t", value, ok)
	}
}

// failingSecretProvider fails the test if it is called at all.
type failingSecretProvider struct{ t *testing.T }

func (f *failingSecretProvider) FetchSecrets(context.Context, []int32) (map[int32]string, error) {
	f.t.Error("provider was contacted although every id was already held locally")
	return nil, context.Canceled
}

// Only the ids not already held are requested, so a partially-cached config
// costs one narrow fetch rather than a full refetch.
func TestEnsureSecretsReadyFetchesOnlyMissingIDs(t *testing.T) {
	persistence := newFakePersistence()
	persistence.secrets[1] = "cached"
	fake := &fakeSecretProvider{}
	inputs, err := NewPersistent(nil, fake, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}

	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(1, 2)); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if !reflect.DeepEqual(fake.ids, []int32{2}) {
		t.Fatalf("fetched ids = %#v, want [2]", fake.ids)
	}
	if value, ok := inputs.ResolveSecret(1); !ok || value != "cached" {
		t.Fatalf("ResolveSecret(1) = %q, %t; want cached, true", value, ok)
	}
}

// A load failure must leave a usable, empty RuntimeInputs rather than a nil one,
// so the caller can log and carry on with the pre-persistence behaviour.
func TestNewPersistentStaysUsableWhenLoadFails(t *testing.T) {
	persistence := newFakePersistence()
	persistence.loadErr = context.DeadlineExceeded
	fake := &fakeSecretProvider{}

	inputs, err := NewPersistent(nil, fake, nil, persistence)
	if err == nil {
		t.Fatal("expected NewPersistent to report the load failure")
	}
	if inputs == nil {
		t.Fatal("NewPersistent returned no RuntimeInputs to fall back on")
	}
	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(1)); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if !reflect.DeepEqual(fake.ids, []int32{1}) {
		t.Fatalf("fetched ids = %#v, want [1]", fake.ids)
	}
}

func TestRetainDropsUnreferencedValuesFromMemoryAndPersistence(t *testing.T) {
	persistence := newFakePersistence()
	inputs, err := NewPersistent(nil, &fakeSecretProvider{}, &fakeConfigProvider{}, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(1, 2)); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}

	if _, err := inputs.Retain(map[int32]struct{}{1: {}}, map[int32]struct{}{}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, ok := inputs.ResolveSecret(2); ok {
		t.Fatal("unreferenced secret still resolvable in memory")
	}
	if _, ok := persistence.secrets[2]; ok {
		t.Fatal("unreferenced secret still persisted")
	}
	if _, ok := inputs.ResolveSecret(1); !ok {
		t.Fatal("referenced secret was dropped")
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
