package preparer

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeSecretProvider struct {
	keys []string
}

type fakeConfigProvider struct {
	keys []string
}

func (f *fakeSecretProvider) FetchSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	f.keys = append([]string(nil), keys...)
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = "value-" + key
	}
	return values, nil
}

func (f *fakeConfigProvider) FetchConfigs(ctx context.Context, keys []string) (map[string]string, error) {
	f.keys = append([]string(nil), keys...)
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = "value-" + key
	}
	return values, nil
}

func TestSecretRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
			{Key: "DB", Value: "${s:db.pass}"},
			{Key: "MIX", Value: "${c:host}:${s: api.token }:${s:db.pass}"},
			{Key: "ESCAPED", Value: "$${s:not.secret}"},
			{Key: "OTHER", Value: "prefix-${s:other}-suffix"},
		}},
	}}}

	want := []string{"api.token", "db.pass", "other"}
	if got := SecretRefs(dep); !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs() = %#v; want %#v", got, want)
	}
}

func TestConfigRefsFindsUniqueSortedEnvRefs(t *testing.T) {
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
			{Key: "URL", Value: "${c:host}:${c: port }:${s:secret}"},
			{Key: "DUP", Value: "${c:host}"},
			{Key: "ESCAPED", Value: "$${c:not.config}"},
			{Key: "OTHER", Value: "prefix-${c:other}-suffix"},
		}},
	}}}

	want := []string{"host", "other", "port"}
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
		Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
			{Key: "A", Value: "${s:a}"},
			{Key: "B", Value: "${s:b}"},
		}},
	}}}

	if err := EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	want := []string{"a", "b"}
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
		Container: apigen.ContainerRunnerConfig{Env: []*apigen.EnvVar{
			{Key: "A", Value: "${c:a}"},
			{Key: "B", Value: "${c:b}"},
		}},
	}}}

	if err := EnsureConfigsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureConfigsReady: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(fake.keys, want) {
		t.Fatalf("fetched keys = %#v; want %#v", fake.keys, want)
	}
}
