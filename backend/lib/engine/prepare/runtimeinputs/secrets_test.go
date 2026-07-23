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
	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
			"DB":    {SecretID: ptrInt32(6)},
			"MIX":   {ConfigID: ptrInt32(3)},
			"TOKEN": {SecretID: ptrInt32(2)},
			"DUP":   {SecretID: ptrInt32(6)},
		}},
	}}}

	want := []int32{2, 6}
	if got := SecretRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs() = %#v; want %#v", got, want)
	}
}

func TestConfigRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
			"URL":    {ConfigID: ptrInt32(18)},
			"DUP":    {ConfigID: ptrInt32(18)},
			"OTHER":  {ConfigID: ptrInt32(2)},
			"SECRET": {SecretID: ptrInt32(9)},
		}},
	}}}

	want := []int32{2, 18}
	if got := ConfigRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigRefs() = %#v; want %#v", got, want)
	}
}

func TestRequiredAssetRefsIncludesExplicitAndEnvAssets(t *testing.T) {
	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{
			AssetMounts: []*apigen.AssetMount2{{AssetID: 8, Permission: apigen.FilePermission_READ_EXECUTE}},
			EnvVars: map[string]*apigen.EnvVarValue2{
				"APP_CONFIG": {Asset: "implicit.conf", AssetID: 12},
				"PLAIN":      {Value: ptrString("value")},
			},
		},
	}}}

	refs := RequiredAssetRefs(dep)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d; want 2", len(refs))
	}
	if refs[0].AssetID != 8 || refs[0].Label != "asset mount 8" || !refs[0].Executable {
		t.Fatalf("refs[0] = %+v", refs[0])
	}
	if refs[1].AssetID != 12 || refs[1].Label != `asset env var "APP_CONFIG"` || refs[1].Executable {
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

	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
			"A": {SecretID: ptrInt32(1)},
			"B": {SecretID: ptrInt32(2)},
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

	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
			"A": {ConfigID: ptrInt32(1)},
			"B": {ConfigID: ptrInt32(2)},
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
	dep := &apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: map[string]*apigen.EnvVarValue2{
			"A": {SecretID: ptrInt32(1)},
			"B": {SecretID: ptrInt32(2)},
		}},
	}}}

	if err := inputs.EnsureSecretsReady(context.Background(), dep); err == nil {
		t.Fatal("expected incomplete secret batch to fail")
	}
	if _, ok := inputs.ResolveSecret(1); ok {
		t.Fatal("incomplete secret batch was cached")
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
