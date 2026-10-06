package runner

import (
	"context"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

type fakeRuntimeInputProvider struct {
	secrets map[apigen.ValueRef]string
	configs map[apigen.ValueRef]string
}

func (f fakeRuntimeInputProvider) FetchSecrets(context.Context, []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	return f.secrets, nil
}

func (f fakeRuntimeInputProvider) FetchConfigs(context.Context, []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	return f.configs, nil
}

func TestResolveEnv(t *testing.T) {
	in := map[string]apigen.EnvVar{
		"PLAIN":   literalEnv("value"),
		"DB_PASS": secretEnv(1, 1),
		"TOKEN":   secretEnv(2, 1),
		"HOST":    configEnv(3, 1),
		"CONFIG":  assetEnv("app.conf", 12, 1),
	}
	provider := fakeRuntimeInputProvider{
		secrets: map[apigen.ValueRef]string{{ID: 1, Version: 1}: "s3cret", {ID: 2, Version: 1}: "abc"},
		configs: map[apigen.ValueRef]string{{ID: 3, Version: 1}: "db.local"},
	}
	inputs := runtimeinputs.New(nil, provider, provider)
	dep := containerDeployment(0, 0, apigen.ContainerRuntime{EnvVars: in})
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
		"CONFIG=/opendeploy-env-assets/12_1",
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
	if _, err := resolveEnv(inputs, map[string]apigen.EnvVar{"X": secretEnv(1, 1)}); err == nil {
		t.Fatal("expected error for unknown secret")
	}
}

func TestResolveEnvUnknownConfigFailsClosed(t *testing.T) {
	inputs := runtimeinputs.New(nil, nil, nil)
	if _, err := resolveEnv(inputs, map[string]apigen.EnvVar{"X": configEnv(1, 1)}); err == nil {
		t.Fatal("expected error for unknown config")
	}
}

func TestResolveEnvRejectsAmbiguousValue(t *testing.T) {
	ambiguous := literalEnv("plain")
	ambiguous.Value.Secret = &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: 1, Version: 1}}
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{"X": ambiguous}); err == nil {
		t.Fatal("expected error for ambiguous env value")
	}
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{"X": {}}); err == nil {
		t.Fatal("expected error for an empty env value")
	}
}

func TestResolveEnvRejectsUnresolvedAsset(t *testing.T) {
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{"X": assetEnv("app.conf", 0, 0)}); err == nil {
		t.Fatal("expected error for unresolved asset")
	}
}

func TestResolveEnvAddressRefDerivesStableAddress(t *testing.T) {
	prefix := network.Prefix{0xfd, 0xab, 0xcd, 0xef, 0x12, 0x34}
	previous := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(previous) })

	out, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{
		"UPSTREAM_ADDR": addressEnv(7, 5),
	})
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	addr, err := prefix.InboundAddr(5, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"UPSTREAM_ADDR=" + addr.String()}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("resolveEnv() = %#v; want %#v", out, want)
	}
}

func TestResolveEnvAddressRefFailsClosed(t *testing.T) {
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{
		"UPSTREAM_ADDR": addressEnv(0, 5),
	}); err == nil {
		t.Fatal("expected address reference without a deployment to fail")
	}
}

func TestResolveEnvAddressRefRejectsOutOfRangeIdentity(t *testing.T) {
	prefix := network.GeneratePrefix()
	previous := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(previous) })

	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]apigen.EnvVar{
		"UPSTREAM_ADDR": addressEnv(uint64(network.MaxDeploymentID)+1, 1),
	}); err == nil {
		t.Fatal("expected oversized deployment id to fail")
	}
}

func TestContainerMountsIncludesImplicitAssetEnvMount(t *testing.T) {
	dep := containerDeployment(0, 0, apigen.ContainerRuntime{DefaultVolume: apigen.DefaultVolumeMount{Disabled: true}, EnvVars: map[string]apigen.EnvVar{"APP_CONFIG": assetEnv("app.conf", 12, 1), "APP_CONFIG_2": assetEnv("app.conf", 12, 1)}})
	mounts, _ := containerMounts(dep)
	if len(mounts) != 1 {
		t.Fatalf("mounts len = %d; want 1", len(mounts))
	}
	if mounts[0].Dest != "/opendeploy-env-assets/12_1" || !mounts[0].ReadOnly {
		t.Fatalf("implicit mount = %+v", mounts[0])
	}
}

func literalEnv(value string) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Literal: &apigen.LiteralEnv{Value: value}}}
}

func secretEnv(id uint64, version uint32) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: id, Version: version}}}}
}

func configEnv(id uint64, version uint32) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: id, Version: version}}}}
}

func assetEnv(key string, id uint64, version uint32) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Asset: &apigen.AssetEnv{Key: key, Asset: apigen.AssetRef{AssetID: id, Version: version}}}}
}

func addressEnv(deploymentID, spaceID uint64) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Address: &apigen.AddressEnv{DeploymentID: deploymentID, SpaceID: spaceID}}}
}

func containerDeployment(id uint64, specVersion uint32, runtime apigen.ContainerRuntime) *apigen.DeploymentRecord {
	return &apigen.DeploymentRecord{
		Deployment: apigen.Deployment{
			ID:   id,
			Name: "app",
			Spec: apigen.DeploymentSpec{
				Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
					Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "registry.example/app"}}},
					Runtime:         runtime,
					Version:         "v1",
					UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
				}}},
				Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
			},
			Scheduling: apigen.DedicatedScheduling(true, 1),
		},
		Meta: apigen.EntityMeta{Version: specVersion, SpecVersion: specVersion},
	}
}
