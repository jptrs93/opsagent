package runner

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
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
	if v.SecretID != nil {
		set++
	}
	if v.ConfigID != nil {
		set++
	}
	if v.AssetID > 0 {
		set++
	}
	if set != 1 {
		return "", fmt.Errorf("exactly one of value, secretId, configId, or asset is required")
	}
	if v.Value != nil {
		return *v.Value, nil
	}
	if v.SecretID != nil {
		return resolveSecretRef(inputs, *v.SecretID)
	}
	if v.AssetID > 0 {
		return implicitAssetContainerPath(v.AssetID), nil
	}
	return resolveConfigRef(inputs, *v.ConfigID)
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
