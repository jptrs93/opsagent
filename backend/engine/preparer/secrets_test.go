package preparer

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeSecretProvider struct {
	keys []int32
}

type fakeConfigProvider struct {
	keys []int32
}

func (f *fakeSecretProvider) FetchSecrets(ctx context.Context, keys []int32) (map[int32]string, error) {
	f.keys = append([]int32(nil), keys...)
	values := make(map[int32]string, len(keys))
	for _, key := range keys {
		values[key] = "value"
	}
	return values, nil
}

func (f *fakeConfigProvider) FetchConfigs(ctx context.Context, keys []int32) (map[int32]string, error) {
	f.keys = append([]int32(nil), keys...)
	values := make(map[int32]string, len(keys))
	for _, key := range keys {
		values[key] = "value"
	}
	return values, nil
}

func TestSecretRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{
			EnvVars: map[string]*apigen.EnvVarValue{
				"DB":    {SecretID: ptrInt32(6)},
				"MIX":   {ConfigID: ptrInt32(3)},
				"TOKEN": {SecretID: ptrInt32(2)},
				"DUP":   {SecretID: ptrInt32(6)},
			},
			OpenobserveConsumer: &apigen.OpenObserveConsumerConfig{TokenSecretID: 4, SaEmailValue: &apigen.EnvVarValue{SecretID: ptrInt32(7)}},
		},
	}}}

	want := []int32{2, 4, 6, 7}
	if got := SecretRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs() = %#v; want %#v", got, want)
	}
}

func TestConfigRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{
			EnvVars: map[string]*apigen.EnvVarValue{
				"URL":    {ConfigID: ptrInt32(18)},
				"DUP":    {ConfigID: ptrInt32(18)},
				"OTHER":  {ConfigID: ptrInt32(2)},
				"SECRET": {SecretID: ptrInt32(9)},
			},
			OpenobserveConsumer: &apigen.OpenObserveConsumerConfig{SaEmailValue: &apigen.EnvVarValue{ConfigID: ptrInt32(33)}},
		},
	}}}

	want := []int32{2, 18, 33}
	if got := ConfigRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigRefs() = %#v; want %#v", got, want)
	}
}

func TestRequiredAssetRefsIncludesExplicitAndEnvAssets(t *testing.T) {
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{
			AssetMounts: []*apigen.ContainerAssetMount{{Asset: "explicit.conf", AssetID: 8, Version: 2}},
			EnvVars: map[string]*apigen.EnvVarValue{
				"APP_CONFIG": {Asset: "implicit.conf", AssetID: 12, Version: 4},
				"PLAIN":      {Value: ptrString("value")},
			},
		},
	}}}

	refs := RequiredAssetRefs(dep)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d; want 2", len(refs))
	}
	if refs[0].AssetID != 8 || refs[0].Version != 2 || refs[0].Label != `asset mount "explicit.conf"` {
		t.Fatalf("refs[0] = %+v", refs[0])
	}
	if refs[1].AssetID != 12 || refs[1].Version != 4 || refs[1].Label != `asset env var "APP_CONFIG"` {
		t.Fatalf("refs[1] = %+v", refs[1])
	}
}

func TestEnsureSecretsReadyFetchesBatch(t *testing.T) {
	prev := Secrets
	fake := &fakeSecretProvider{}
	Secrets = fake
	defer func() { Secrets = prev }()

	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
			"A": {SecretID: ptrInt32(1)},
			"B": {SecretID: ptrInt32(2)},
		}},
	}}}

	if err := EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	want := []int32{1, 2}
	if !reflect.DeepEqual(fake.keys, want) {
		t.Fatalf("fetched keys = %#v; want %#v", fake.keys, want)
	}
}

func TestEnsureConfigsReadyFetchesBatch(t *testing.T) {
	prev := Configs
	fake := &fakeConfigProvider{}
	Configs = fake
	defer func() { Configs = prev }()

	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
			"A": {ConfigID: ptrInt32(1)},
			"B": {ConfigID: ptrInt32(2)},
		}},
	}}}

	if err := EnsureConfigsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureConfigsReady: %v", err)
	}
	want := []int32{1, 2}
	if !reflect.DeepEqual(fake.keys, want) {
		t.Fatalf("fetched keys = %#v; want %#v", fake.keys, want)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }
