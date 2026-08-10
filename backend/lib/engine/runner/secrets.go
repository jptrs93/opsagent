package runner

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

const implicitAssetContainerDir = "/opendeploy-env-assets"

func resolveEnv(inputs *runtimeinputs.RuntimeInputs, env map[string]*apigen.EnvVarValue) ([]string, error) {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		val, err := resolveEnvValue(inputs, key, env[key])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
		out = append(out, key+"="+val)
	}
	return out, nil
}

func resolveEnvValue(inputs *runtimeinputs.RuntimeInputs, key string, v *apigen.EnvVarValue) (string, error) {
	if v == nil {
		return "", fmt.Errorf("value is required")
	}
	set := 0
	if v.Value != nil {
		set++
	}
	if v.SecretVersionID != nil {
		set++
	}
	if v.ConfigVersionID != nil {
		set++
	}
	if v.AssetVersionID > 0 {
		set++
	}
	if v.AddressDeploymentID != nil || v.AddressSpaceID != nil {
		set++
	}
	if set != 1 {
		return "", fmt.Errorf("exactly one of value, secretId, configId, asset, or address is required")
	}
	if v.Value != nil {
		return *v.Value, nil
	}
	if v.SecretVersionID != nil {
		return resolveSecretRef(inputs, *v.SecretVersionID)
	}
	if v.AssetVersionID > 0 {
		return implicitAssetContainerPath(v.AssetVersionID), nil
	}
	if v.AddressDeploymentID != nil || v.AddressSpaceID != nil {
		return resolveAddressRef(v)
	}
	return resolveConfigRef(inputs, *v.ConfigVersionID)
}

func resolveAddressRef(v *apigen.EnvVarValue) (string, error) {
	if v.AddressDeploymentID == nil || v.AddressSpaceID == nil {
		return "", fmt.Errorf("addressDeploymentId and addressSpaceId are required together")
	}
	if *v.AddressDeploymentID <= 0 || *v.AddressDeploymentID > network.MaxDeploymentID ||
		*v.AddressSpaceID < 0 || *v.AddressSpaceID > network.MaxSpaceID {
		return "", fmt.Errorf("invalid address reference")
	}
	prefix, ok := network.Default.PrefixValue()
	if !ok {
		return "", fmt.Errorf("cluster network prefix is unavailable")
	}
	addr, err := prefix.InboundAddr(*v.AddressSpaceID, *v.AddressDeploymentID, 0)
	if err != nil {
		return "", fmt.Errorf("derive deployment address: %w", err)
	}
	return addr.String(), nil
}

func implicitAssetContainerPath(assetID int32) string {
	return implicitAssetContainerDir + "/" + strconv.Itoa(int(assetID))
}

func resolveSecretRef(inputs *runtimeinputs.RuntimeInputs, id int32) (string, error) {
	val, ok := inputs.ResolveSecret(id)
	if !ok {
		return "", fmt.Errorf("unknown secret id %d", id)
	}
	return val, nil
}

func resolveConfigRef(inputs *runtimeinputs.RuntimeInputs, id int32) (string, error) {
	val, ok := inputs.ResolveConfig(id)
	if !ok {
		return "", fmt.Errorf("unknown config id %d", id)
	}
	return val, nil
}
