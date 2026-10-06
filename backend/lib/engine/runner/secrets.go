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

func resolveEnv(inputs *runtimeinputs.RuntimeInputs, env map[string]apigen.EnvVar) ([]string, error) {
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

func resolveEnvValue(inputs *runtimeinputs.RuntimeInputs, key string, v apigen.EnvVar) (string, error) {
	set := 0
	if v.Value.Literal != nil {
		set++
	}
	if v.Value.Secret != nil {
		set++
	}
	if v.Value.Config != nil {
		set++
	}
	if v.Value.Asset != nil {
		set++
	}
	if v.Value.Address != nil {
		set++
	}
	if set != 1 {
		return "", fmt.Errorf("exactly one of literal, secret, config, asset, or address is required")
	}
	switch {
	case v.Value.Literal != nil:
		return v.Value.Literal.Value, nil
	case v.Value.Secret != nil:
		return resolveSecretRef(inputs, v.Value.Secret.Secret.Ref())
	case v.Value.Asset != nil:
		if !v.Value.Asset.Asset.Valid() {
			return "", fmt.Errorf("asset reference is unresolved")
		}
		return implicitAssetContainerPath(v.Value.Asset.Asset.Ref()), nil
	case v.Value.Address != nil:
		return resolveAddressRef(v.Value.Address)
	default:
		return resolveConfigRef(inputs, v.Value.Config.Config.Ref())
	}
}

func resolveAddressRef(v *apigen.AddressEnv) (string, error) {
	if v.DeploymentID == 0 || v.DeploymentID > uint64(network.MaxDeploymentID) || v.SpaceID > uint64(network.MaxSpaceID) {
		return "", fmt.Errorf("invalid address reference")
	}
	prefix, ok := network.Default.PrefixValue()
	if !ok {
		return "", fmt.Errorf("cluster network prefix is unavailable")
	}
	addr, err := prefix.InboundAddr(int32(v.SpaceID), int32(v.DeploymentID), 0)
	if err != nil {
		return "", fmt.Errorf("derive deployment address: %w", err)
	}
	return addr.String(), nil
}

func implicitAssetContainerPath(ref apigen.ValueRef) string {
	return implicitAssetContainerDir + "/" + strconv.FormatUint(ref.ID, 10) + "_" + strconv.FormatUint(uint64(ref.Version), 10)
}

func resolveSecretRef(inputs *runtimeinputs.RuntimeInputs, ref apigen.ValueRef) (string, error) {
	val, ok := inputs.ResolveSecret(ref)
	if !ok {
		return "", fmt.Errorf("unknown secret %s", ref)
	}
	return val, nil
}

func resolveConfigRef(inputs *runtimeinputs.RuntimeInputs, ref apigen.ValueRef) (string, error) {
	val, ok := inputs.ResolveConfig(ref)
	if !ok {
		return "", fmt.Errorf("unknown config %s", ref)
	}
	return val, nil
}
