package runtimeinputs

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeSecretProvider struct {
	refs   []apigen.ValueRef
	values map[apigen.ValueRef]string
}

type fakeConfigProvider struct {
	refs   []apigen.ValueRef
	values map[apigen.ValueRef]string
}

func (f *fakeSecretProvider) FetchSecrets(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	f.refs = append([]apigen.ValueRef(nil), refs...)
	if f.values != nil {
		return f.values, nil
	}
	values := make(map[apigen.ValueRef]string, len(refs))
	for _, ref := range refs {
		values[ref] = "value"
	}
	return values, nil
}

func (f *fakeConfigProvider) FetchConfigs(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	f.refs = append([]apigen.ValueRef(nil), refs...)
	if f.values != nil {
		return f.values, nil
	}
	values := make(map[apigen.ValueRef]string, len(refs))
	for _, ref := range refs {
		values[ref] = "value"
	}
	return values, nil
}

func TestSecretRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{"DB": {Secret: ref(6, 1)}, "MIX": {Config: ref(3, 1)}, "TOKEN": {Secret: ref(2, 1)}, "DUP": {Secret: ref(6, 1)}}}}}},
	}

	want := []apigen.ValueRef{vr(2), vr(6)}
	if got := SecretRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs() = %#v; want %#v", got, want)
	}
}

func TestConfigRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{"URL": {Config: ref(18, 1)}, "DUP": {Config: ref(18, 1)}, "OTHER": {Config: ref(2, 1)}, "SECRET": {Secret: ref(9, 1)}}}}}},
	}

	want := []apigen.ValueRef{vr(2), vr(18)}
	if got := ConfigRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigRefs() = %#v; want %#v", got, want)
	}
}

func TestRequiredAssetRefsIncludesExplicitAndEnvAssets(t *testing.T) {
	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{AssetMounts: []*apigen.AssetMount{{Asset: vr(8), Permission: apigen.FilePermission_READ_EXECUTE}}, EnvVars: map[string]*apigen.EnvVarValue{"APP_CONFIG": {Asset: "implicit.conf", AssetRef: ref(12, 1)}, "PLAIN": {Value: ptrString("value")}}}}}},
	}

	refs := RequiredAssetRefs(dep)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d; want 2", len(refs))
	}
	if refs[0].Ref != vr(8) || refs[0].Label != "asset mount 8@1" || !refs[0].Executable {
		t.Fatalf("refs[0] = %+v", refs[0])
	}
	if refs[1].Ref != vr(12) || refs[1].Label != `asset env var "APP_CONFIG"` || refs[1].Executable {
		t.Fatalf("refs[1] = %+v", refs[1])
	}
}

func TestAssetCachePathWithModeUsesSeparateExecutablePath(t *testing.T) {
	readonly := AssetCachePathWithMode(vr(8), false)
	executable := AssetCachePathWithMode(vr(8), true)
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

	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{"A": {Secret: ref(1, 1)}, "B": {Secret: ref(2, 1)}}}}}},
	}

	if err := inputs.EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	want := []apigen.ValueRef{vr(1), vr(2)}
	if !reflect.DeepEqual(fake.refs, want) {
		t.Fatalf("fetched refs = %#v; want %#v", fake.refs, want)
	}
	if value, ok := inputs.ResolveSecret(vr(2)); !ok || value != "value" {
		t.Fatalf("ResolveSecret(2) = %q, %t; want value, true", value, ok)
	}
}

func TestEnsureConfigsReadyFetchesBatch(t *testing.T) {
	fake := &fakeConfigProvider{}
	inputs := New(nil, nil, fake)

	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{"A": {Config: ref(1, 1)}, "B": {Config: ref(2, 1)}}}}}},
	}

	if err := inputs.EnsureConfigsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureConfigsReady: %v", err)
	}
	want := []apigen.ValueRef{vr(1), vr(2)}
	if !reflect.DeepEqual(fake.refs, want) {
		t.Fatalf("fetched refs = %#v; want %#v", fake.refs, want)
	}
	if value, ok := inputs.ResolveConfig(vr(2)); !ok || value != "value" {
		t.Fatalf("ResolveConfig(2) = %q, %t; want value, true", value, ok)
	}
}

func TestEnsureSecretsReadyDoesNotCacheIncompleteBatch(t *testing.T) {
	fake := &fakeSecretProvider{values: map[apigen.ValueRef]string{vr(1): "one"}}
	inputs := New(nil, fake, nil)
	dep := &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue{"A": {Secret: ref(1, 1)}, "B": {Secret: ref(2, 1)}}}}}},
	}

	if err := inputs.EnsureSecretsReady(context.Background(), dep); err == nil {
		t.Fatal("expected incomplete secret batch to fail")
	}
	if _, ok := inputs.ResolveSecret(vr(1)); ok {
		t.Fatal("incomplete secret batch was cached")
	}
}

// fakePersistence records what was written so tests can assert the durable side
// independently of the in-memory maps.
type fakePersistence struct {
	secrets map[apigen.ValueRef]string
	configs map[apigen.ValueRef]string
	loadErr error
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{secrets: map[apigen.ValueRef]string{}, configs: map[apigen.ValueRef]string{}}
}

func (f *fakePersistence) LoadRuntimeInputs() (map[apigen.ValueRef]string, map[apigen.ValueRef]string, error) {
	if f.loadErr != nil {
		return nil, nil, f.loadErr
	}
	secrets := map[apigen.ValueRef]string{}
	configs := map[apigen.ValueRef]string{}
	for ref, v := range f.secrets {
		secrets[ref] = v
	}
	for ref, v := range f.configs {
		configs[ref] = v
	}
	return secrets, configs, nil
}

func (f *fakePersistence) StoreRuntimeInputs(secrets, configs map[apigen.ValueRef]string) error {
	for ref, v := range secrets {
		f.secrets[ref] = v
	}
	for ref, v := range configs {
		f.configs[ref] = v
	}
	return nil
}

func (f *fakePersistence) RetainRuntimeInputs(secrets, configs map[apigen.ValueRef]struct{}) (int, error) {
	removed := 0
	for ref := range f.secrets {
		if _, ok := secrets[ref]; !ok {
			delete(f.secrets, ref)
			removed++
		}
	}
	for ref := range f.configs {
		if _, ok := configs[ref]; !ok {
			delete(f.configs, ref)
			removed++
		}
	}
	return removed, nil
}

func secretRefDeployment(refs ...apigen.ValueRef) *apigen.DeploymentEvent {
	env := map[string]*apigen.EnvVarValue{}
	for i, ref := range refs {
		ref := ref
		env[string(rune('A'+i))] = &apigen.EnvVarValue{Secret: &ref}
	}
	return &apigen.DeploymentEvent{
		Value: apigen.Deployment{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Runtime: apigen.ContainerRuntime{EnvVars: env}}}},
	}
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
	dep := secretRefDeployment(vr(1), vr(2))
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
	if value, ok := restarted.ResolveSecret(vr(2)); !ok || value != "value" {
		t.Fatalf("ResolveSecret(2) after restart = %q, %t", value, ok)
	}
}

// failingSecretProvider fails the test if it is called at all.
type failingSecretProvider struct{ t *testing.T }

func (f *failingSecretProvider) FetchSecrets(context.Context, []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	f.t.Error("provider was contacted although every id was already held locally")
	return nil, context.Canceled
}

// Only the refs not already held are requested, so a partially-cached config
// costs one narrow fetch rather than a full refetch. Another value version of a
// held secret is a different ref and is fetched.
func TestEnsureSecretsReadyFetchesOnlyMissingRefs(t *testing.T) {
	persistence := newFakePersistence()
	persistence.secrets[vr(1)] = "cached"
	fake := &fakeSecretProvider{}
	inputs, err := NewPersistent(nil, fake, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}

	next := apigen.ValueRef{ID: 1, Version: 2}
	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(vr(1), next, vr(2))); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if want := []apigen.ValueRef{next, vr(2)}; !reflect.DeepEqual(fake.refs, want) {
		t.Fatalf("fetched refs = %#v, want %#v", fake.refs, want)
	}
	if value, ok := inputs.ResolveSecret(vr(1)); !ok || value != "cached" {
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
	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(vr(1))); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if !reflect.DeepEqual(fake.refs, []apigen.ValueRef{vr(1)}) {
		t.Fatalf("fetched refs = %#v, want [1@1]", fake.refs)
	}
}

func TestRetainDropsUnreferencedValuesFromMemoryAndPersistence(t *testing.T) {
	persistence := newFakePersistence()
	inputs, err := NewPersistent(nil, &fakeSecretProvider{}, &fakeConfigProvider{}, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if err := inputs.EnsureSecretsReady(context.Background(), secretRefDeployment(vr(1), vr(2))); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}

	if _, err := inputs.Retain(map[apigen.ValueRef]struct{}{vr(1): {}}, map[apigen.ValueRef]struct{}{}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, ok := inputs.ResolveSecret(vr(2)); ok {
		t.Fatal("unreferenced secret still resolvable in memory")
	}
	if _, ok := persistence.secrets[vr(2)]; ok {
		t.Fatal("unreferenced secret still persisted")
	}
	if _, ok := inputs.ResolveSecret(vr(1)); !ok {
		t.Fatal("referenced secret was dropped")
	}
}

func ptrInt32(v int32) *int32     { return &v }
func vr(id int32) apigen.ValueRef { return apigen.ValueRef{ID: id, Version: 1} }
func ref(id, version int32) *apigen.ValueRef {
	return &apigen.ValueRef{ID: id, Version: version}
}
func ptrString(v string) *string { return &v }
