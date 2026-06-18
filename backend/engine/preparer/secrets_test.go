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
		Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
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
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{EnvVars: map[string]*apigen.EnvVarValue{
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

func ptrInt32(v int32) *int32 { return &v }
