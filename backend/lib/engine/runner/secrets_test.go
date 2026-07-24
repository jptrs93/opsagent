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
	dep := &apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Runtime: apigen.ContainerRuntime{EnvVars: in},
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

func TestResolveEnvAddressRefDerivesStableAddress(t *testing.T) {
	prefix := network.Prefix{0xfd, 0xab, 0xcd, 0xef, 0x12, 0x34}
	previous := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(previous) })

	deploymentID := int32(7)
	spaceID := int32(5)
	out, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]*apigen.EnvVarValue{
		"UPSTREAM_ADDR": {AddressDeploymentID: &deploymentID, AddressSpaceID: &spaceID},
	})
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	addr, err := prefix.InboundAddr(spaceID, deploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"UPSTREAM_ADDR=" + addr.String()}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("resolveEnv() = %#v; want %#v", out, want)
	}
}

func TestResolveEnvAddressRefFailsClosed(t *testing.T) {
	deploymentID := int32(7)
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]*apigen.EnvVarValue{
		"UPSTREAM_ADDR": {AddressDeploymentID: &deploymentID},
	}); err == nil {
		t.Fatal("expected incomplete address reference to fail")
	}
}

func TestResolveEnvAddressRefRejectsOutOfRangeIdentity(t *testing.T) {
	prefix := network.GeneratePrefix()
	previous := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(previous) })

	deploymentID := network.MaxDeploymentID + 1
	spaceID := int32(1)
	if _, err := resolveEnv(runtimeinputs.New(nil, nil, nil), map[string]*apigen.EnvVarValue{
		"UPSTREAM_ADDR": {AddressDeploymentID: &deploymentID, AddressSpaceID: &spaceID},
	}); err == nil {
		t.Fatal("expected oversized deployment id to fail")
	}
}

func TestContainerMountsIncludesImplicitAssetEnvMount(t *testing.T) {
	dep := &apigen.DeploymentConfig{
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
			Runtime: apigen.ContainerRuntime{
				DefaultVolume: apigen.DefaultVolumeMount{Disabled: true},
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
