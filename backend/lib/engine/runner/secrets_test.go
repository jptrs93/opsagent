package runner

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
)

type fakeRuntimeInputProvider struct {
	secrets map[int32]string
	configs map[int32]string
}

func (f fakeRuntimeInputProvider) FetchSecrets(context.Context, []int32) (map[int32]string, error) {
	return f.secrets, nil
}

func (f fakeRuntimeInputProvider) FetchConfigs(context.Context, []int32) (map[int32]string, error) {
	return f.configs, nil
}

func TestResolveEnv(t *testing.T) {
	in := map[string]*apigen.EnvVarValue{
		"PLAIN":   {Value: ptrString("value")},
		"DB_PASS": {SecretID: ptrInt32(1)},
		"TOKEN":   {SecretID: ptrInt32(2)},
		"HOST":    {ConfigID: ptrInt32(3)},
		"CONFIG":  {Asset: "app.conf", AssetID: 12},
	}
	provider := fakeRuntimeInputProvider{
		secrets: map[int32]string{1: "s3cret", 2: "abc"},
		configs: map[int32]string{3: "db.local"},
	}
	inputs := runtimeinputs.New(nil, provider, provider)
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
		Container: apigen.ContainerRunnerConfig{EnvVars: in},
	}}}
	if err := inputs.EnsureSecretsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureSecretsReady: %v", err)
	}
	if err := inputs.EnsureConfigsReady(context.Background(), dep); err != nil {
		t.Fatalf("EnsureConfigsReady: %v", err)
	}
	out, err := resolveEnv(inputs, in)
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	want := []string{
		"CONFIG=/opendeploy-env-assets/12",
		"DB_PASS=s3cret",
		"HOST=db.local",
		"PLAIN=value",
		"TOKEN=abc",
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("resolveEnv() = %#v; want %#v", out, want)
	}
}

func TestResolveEnvUnknownSecretFailsClosed(t *testing.T) {
	inputs := runtimeinputs.New(nil, nil, nil)
	if _, err := resolveEnv(inputs, map[string]*apigen.EnvVarValue{"X": {SecretID: ptrInt32(1)}}); err == nil {
		t.Fatal("expected error for unknown secret")
	}
}

func TestResolveEnvUnknownConfigFailsClosed(t *testing.T) {
	inputs := runtimeinputs.New(nil, nil, nil)
	if _, err := resolveEnv(inputs, map[string]*apigen.EnvVarValue{"X": {ConfigID: ptrInt32(1)}}); err == nil {
		t.Fatal("expected error for unknown config")
	}
}

func TestResolveEnvRejectsAmbiguousValue(t *testing.T) {
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]*apigen.EnvVarValue{"X": {Value: ptrString("plain"), SecretID: ptrInt32(1)}}); err == nil {
		t.Fatal("expected error for ambiguous env value")
	}
}

func TestResolveEnvRejectsUnresolvedAsset(t *testing.T) {
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]*apigen.EnvVarValue{"X": {Asset: "app.conf"}}); err == nil {
		t.Fatal("expected error for unresolved asset")
	}
}

func TestContainerMountsIncludesImplicitAssetEnvMount(t *testing.T) {
	dep := &apigen.DeploymentConfig{
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DisableDataVolume: true,
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG":   {Asset: "app.conf", AssetID: 12},
					"APP_CONFIG_2": {Asset: "app.conf", AssetID: 12},
				},
			},
		}},
	}
	mounts, _ := containerMounts(dep)
	if len(mounts) != 1 {
		t.Fatalf("mounts len = %d; want 1", len(mounts))
	}
	if mounts[0].Dest != "/opendeploy-env-assets/12" || !mounts[0].ReadOnly {
		t.Fatalf("implicit mount = %+v", mounts[0])
	}
}

func ptrString(v string) *string { return &v }
func ptrInt32(v int32) *int32    { return &v }
