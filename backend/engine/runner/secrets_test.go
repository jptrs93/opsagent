package runner

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
)

type fakeResolver map[int32]string

func (f fakeResolver) Resolve(id int32) (string, bool)       { v, ok := f[id]; return v, ok }
func (f fakeResolver) ResolveConfig(id int32) (string, bool) { v, ok := f[id]; return v, ok }

func withResolvers(secrets SecretResolver, configs ConfigResolver, fn func()) {
	prevSecrets := Secrets
	prevConfigs := Configs
	Secrets = secrets
	Configs = configs
	defer func() { Secrets = prevSecrets; Configs = prevConfigs }()
	fn()
}

func TestResolveEnv(t *testing.T) {
	withResolvers(fakeResolver{1: "s3cret", 2: "abc"}, fakeResolver{3: "db.local"}, func() {
		in := map[string]*apigen.EnvVarValue{
			"PLAIN":   {Value: ptrString("value")},
			"DB_PASS": {SecretID: ptrInt32(1)},
			"TOKEN":   {SecretID: ptrInt32(2)},
			"HOST":    {ConfigID: ptrInt32(3)},
			"CONFIG":  {Asset: "app.conf", AssetID: 12, Version: 4},
		}
		out, err := resolveEnv(in)
		if err != nil {
			t.Fatalf("resolveEnv: %v", err)
		}
		want := []string{
			"CONFIG=/var/lib/opendeploy-implicit-assets/12_4",
			"DB_PASS=s3cret",
			"HOST=db.local",
			"PLAIN=value",
			"TOKEN=abc",
		}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("resolveEnv() = %#v; want %#v", out, want)
		}
	})
}

func TestResolveEnvUnknownSecretFailsClosed(t *testing.T) {
	withResolvers(fakeResolver{}, fakeResolver{}, func() {
		if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {SecretID: ptrInt32(1)}}); err == nil {
			t.Fatal("expected error for unknown secret")
		}
	})
}

func TestResolveEnvUnknownConfigFailsClosed(t *testing.T) {
	withResolvers(fakeResolver{}, fakeResolver{}, func() {
		if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {ConfigID: ptrInt32(1)}}); err == nil {
			t.Fatal("expected error for unknown config")
		}
	})
}

func TestResolveEnvNoResolverFailsClosed(t *testing.T) {
	withResolvers(nil, nil, func() {
		if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {SecretID: ptrInt32(1)}}); err == nil {
			t.Fatal("expected error when no resolver is set")
		}
		if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {ConfigID: ptrInt32(1)}}); err == nil {
			t.Fatal("expected error when no config resolver is set")
		}
		out, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {Value: ptrString("plain")}})
		if err != nil || out[0] != "X=plain" {
			t.Fatalf("plain passthrough failed: %v %q", err, out)
		}
	})
}

func TestResolveEnvRejectsAmbiguousValue(t *testing.T) {
	if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {Value: ptrString("plain"), SecretID: ptrInt32(1)}}); err == nil {
		t.Fatal("expected error for ambiguous env value")
	}
}

func TestResolveEnvRejectsUnresolvedAsset(t *testing.T) {
	if _, err := resolveEnv(map[string]*apigen.EnvVarValue{"X": {Asset: "app.conf"}}); err == nil {
		t.Fatal("expected error for unresolved asset")
	}
}

func TestContainerMountsIncludesImplicitAssetEnvMount(t *testing.T) {
	dep := &apigen.DeploymentConfig{
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DisableDataVolume: true,
				EnvVars: map[string]*apigen.EnvVarValue{
					"APP_CONFIG":   {Asset: "app.conf", AssetID: 12, Version: 4},
					"APP_CONFIG_2": {Asset: "app.conf", AssetID: 12, Version: 4},
				},
			},
		}},
	}
	mounts, _ := containerMounts(dep)
	if len(mounts) != 1 {
		t.Fatalf("mounts len = %d; want 1", len(mounts))
	}
	if mounts[0].Dest != "/var/lib/opendeploy-implicit-assets/12_4" || !mounts[0].ReadOnly {
		t.Fatalf("implicit mount = %+v", mounts[0])
	}
}

func TestBuildContainerRunnerUsesConfiguredLogConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dep := &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DisableDataVolume: true,
				LogConsumer:       apigen.ContainerLogConsumer_JSON,
			},
		}},
	}

	r := buildContainerRunner(ctx, cancel, nil, dep, 26)
	if r.logConsumer != ctrd.LogConsumerJSON {
		t.Fatalf("log consumer = %v, want %v", r.logConsumer, ctrd.LogConsumerJSON)
	}
}

func TestBuildContainerRunnerKeepsOpenObserveConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dep := &apigen.DeploymentConfig{
		ID: 7,
		Spec: apigen.DeploymentSpec{Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				DisableDataVolume: true,
				LogConsumer:       apigen.ContainerLogConsumer_OPENOBSERVE,
				OpenobserveConsumer: &apigen.OpenObserveConsumerConfig{
					Url:           "https://logs.example.com",
					Stream:        "api",
					TokenSecretID: 12,
				},
			},
		}},
	}

	r := buildContainerRunner(ctx, cancel, nil, dep, 26)
	if r.logConsumer != ctrd.LogConsumerOpenObserve {
		t.Fatalf("log consumer = %v, want %v", r.logConsumer, ctrd.LogConsumerOpenObserve)
	}
	if r.openObserve == nil || r.openObserve.TokenSecretID != 12 {
		t.Fatalf("openobserve config = %#v", r.openObserve)
	}
}

func ptrString(v string) *string { return &v }
func ptrInt32(v int32) *int32    { return &v }
