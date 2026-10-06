package legacyconv

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func TestHistoricEnvVarDisplayKey(t *testing.T) {
	literal := "v"
	old := apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{
		Source: remoteSpec().Source,
		Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{
			"KEEP":  {Value: &literal},
			"MODEL": {Asset: "price_model_single_item.pt"},
		}},
	}})}
	t.Run("historic drops the env var and reports it", func(t *testing.T) {
		got, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, old.Encode(), IDs{Historic: true})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{`EnvVarValue[MODEL].asset: dropped, display key "price_model_single_item.pt" without an asset_ref`}
		if !reflect.DeepEqual(repairs, want) {
			t.Fatalf("repairs %q, want %q", repairs, want)
		}
		env := got.Value.Deployment.Spec.Container().Runtime.EnvVars
		if len(env) != 1 || env["KEEP"].Value.Literal == nil || env["KEEP"].Value.Literal.Value != "v" {
			t.Fatalf("env vars after the repair: %+v", env)
		}
	})
	t.Run("strict refuses", func(t *testing.T) {
		_, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, old.Encode(), IDs{})
		if err == nil || !strings.Contains(err.Error(), "without an asset_ref") || repairs != nil {
			t.Fatalf("err %v, repairs %q", err, repairs)
		}
	})
	t.Run("a display key beside a literal is the literal in both modes", func(t *testing.T) {
		unknown := "<unknown ref>"
		mixed := apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{
			Source:  remoteSpec().Source,
			Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{"X": {Value: &unknown, Asset: "k"}}},
		}})}
		for _, historic := range []bool{false, true} {
			got, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, mixed.Encode(), IDs{Historic: historic})
			if err != nil || repairs != nil {
				t.Fatalf("historic %v: err %v, repairs %q", historic, err, repairs)
			}
			env := got.Value.Deployment.Spec.Container().Runtime.EnvVars
			if env["X"].Value.Literal == nil || env["X"].Value.Literal.Value != "<unknown ref>" {
				t.Fatalf("historic %v: env var = %+v", historic, env["X"])
			}
		}
	})
	t.Run("historic still refuses a display key beside a reference arm", func(t *testing.T) {
		mixed := apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{
			Source:  remoteSpec().Source,
			Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{"X": {Secret: &apigenold.ValueRef{ID: 3, Version: 1}, Asset: "k"}}},
		}})}
		_, _, err := Entity(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, mixed.Encode(), IDs{Historic: true})
		if err == nil || !strings.Contains(err.Error(), "without an asset_ref") {
			t.Fatalf("err %v", err)
		}
	})
}

func TestUnspecifiedNetworkingModeRanOnTheHost(t *testing.T) {
	old := apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: remoteSpec().Source}})}
	got, _, err := Entity(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, old.Encode(), IDs{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_HOST {
		t.Fatalf("mode = %v", got.Value.Deployment.Spec.Networking.Mode)
	}
}

func TestHistoricUlaPrefix(t *testing.T) {
	prefix := []byte{0xfd, 1, 2, 3, 4, 5}
	old := apigenold.CoreEntity{SystemConfig: &apigenold.SystemConfig{MasterPasswordHash: "h"}}
	t.Run("historic with a known prefix repairs", func(t *testing.T) {
		got, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, old.Encode(), IDs{Historic: true, UlaPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"SystemConfig.network_ula_prefix: empty, took the latest revision's prefix"}
		if !reflect.DeepEqual(repairs, want) {
			t.Fatalf("repairs %q, want %q", repairs, want)
		}
		if string(got.Value.SystemConfig.NetworkUlaPrefix) != string(prefix) || got.Value.SystemConfig.MasterPasswordHash.Value != "h" {
			t.Fatalf("converted config: %+v", got.Value.SystemConfig)
		}
	})
	t.Run("historic without a prefix refuses", func(t *testing.T) {
		_, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, old.Encode(), IDs{Historic: true})
		if err == nil || !strings.Contains(err.Error(), "network_ula_prefix") || repairs != nil {
			t.Fatalf("err %v, repairs %q", err, repairs)
		}
	})
	t.Run("strict refuses even with a prefix", func(t *testing.T) {
		_, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, old.Encode(), IDs{UlaPrefix: prefix})
		if err == nil || !strings.Contains(err.Error(), "network_ula_prefix") || repairs != nil {
			t.Fatalf("err %v, repairs %q", err, repairs)
		}
	})
	t.Run("historic row with its own prefix is not repaired", func(t *testing.T) {
		own := apigenold.CoreEntity{SystemConfig: &apigenold.SystemConfig{NetworkUlaPrefix: []byte{0xfd, 9, 9, 9, 9, 9}}}
		got, repairs, err := Entity(apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, own.Encode(), IDs{Historic: true, UlaPrefix: prefix})
		if err != nil || repairs != nil || got.Value.SystemConfig.NetworkUlaPrefix[1] != 9 {
			t.Fatalf("err %v, repairs %q, got %+v", err, repairs, got)
		}
	})
}
