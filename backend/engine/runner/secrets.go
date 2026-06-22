package runner

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const implicitAssetContainerDir = "/var/lib/opendeploy-implicit-assets"

// SecretResolver resolves a secret id to its plaintext value. It is set at
// startup (on the primary) to the secrets manager.
type SecretResolver interface {
	Resolve(id int32) (value string, ok bool)
}

// ConfigResolver resolves a plain user config by id. It is intentionally a
// separate interface because config values are not encrypted at rest.
type ConfigResolver interface {
	ResolveConfig(id int32) (value string, ok bool)
}

// Secrets and Configs are process-wide resolvers used to expand typed env refs
// at spawn time. Resolution happens at spawn so referenced values are picked up
// on the next (re)start.
var Secrets SecretResolver
var Configs ConfigResolver

func resolveEnv(env map[string]*apigen.EnvVarValue) ([]string, error) {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		val, err := resolveEnvValue(key, env[key])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
		out = append(out, key+"="+val)
	}
	return out, nil
}

func resolveEnvValue(key string, v *apigen.EnvVarValue) (string, error) {
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
	if v.Asset != "" {
		set++
	}
	if set != 1 {
		return "", fmt.Errorf("exactly one of value, secretId, configId, or asset is required")
	}
	if v.Value != nil {
		return *v.Value, nil
	}
	if v.SecretID != nil {
		return resolveSecretRef(*v.SecretID)
	}
	if v.Asset != "" {
		if v.AssetID <= 0 || v.Version <= 0 {
			return "", fmt.Errorf("asset env var %q has unresolved asset id/version", key)
		}
		return implicitAssetContainerPath(v.AssetID, v.Version), nil
	}
	return resolveConfigRef(*v.ConfigID)
}

func implicitAssetContainerPath(assetID, version int32) string {
	return implicitAssetContainerDir + "/" + strconv.Itoa(int(assetID)) + "_" + strconv.Itoa(int(version))
}

func resolveSecretRef(id int32) (string, error) {
	if Secrets == nil {
		return "", fmt.Errorf("unknown secret id %d: no secrets store on this node", id)
	}
	val, ok := Secrets.Resolve(id)
	if !ok {
		return "", fmt.Errorf("unknown secret id %d", id)
	}
	return val, nil
}

func resolveConfigRef(id int32) (string, error) {
	if Configs == nil {
		return "", fmt.Errorf("unknown config id %d: no config store on this node", id)
	}
	val, ok := Configs.ResolveConfig(id)
	if !ok {
		return "", fmt.Errorf("unknown config id %d", id)
	}
	return val, nil
}
