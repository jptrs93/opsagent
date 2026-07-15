package webuihandler

import (
	"encoding/json"
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type exportedConfigBundle struct {
	Deployments []*apigen.DeploymentConfig `json:"deployments"`
	Configs     []*apigen.UserConfig       `json:"configs"`
	Secrets     []*apigen.SecretMeta       `json:"secrets"`
	Assets      []*apigen.AssetMeta        `json:"assets"`
	Spaces      []*apigen.Space            `json:"spaces"`
	Settings    apigen.Settings            `json:"settings"`
}

func (h *Handler) PostV1GenerateExportedConfig(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.ExportedConfigBlob, error) {
	deployments := h.Store.ListActiveDeploymentConfigs()
	exportedDeployments := make([]*apigen.DeploymentConfig, 0, len(deployments))
	for _, deployment := range deployments {
		if deployment == nil {
			continue
		}
		copy := *deployment
		copy.ConfigID.Machine = ""
		exportedDeployments = append(exportedDeployments, &copy)
	}
	configs := h.Store.ListUserConfigs()
	secrets := h.listSecretMetas()
	assets := h.Store.ListAssets()
	spaces := h.Store.ListSpaces()
	storedSettings := h.ConfigService.Snapshot().Settings
	settings := storedSettings

	sort.Slice(exportedDeployments, func(i, j int) bool { return exportedDeployments[i].ID < exportedDeployments[j].ID })
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
	sort.Slice(assets, func(i, j int) bool { return assets[i].Key < assets[j].Key })
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].ID < spaces[j].ID })

	content := exportedConfigBundle{
		Deployments: exportedDeployments,
		Configs:     configs,
		Secrets:     secrets,
		Assets:      assets,
		Spaces:      spaces,
		Settings:    settings,
	}
	var jsonValue map[string]any
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	if unmarshalErr := json.Unmarshal(encoded, &jsonValue); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	for key, value := range jsonValue {
		jsonValue[key], _ = pruneExportValue(value)
	}
	blob, err := json.MarshalIndent(jsonValue, "", "  ")
	if err != nil {
		return nil, err
	}

	return &apigen.ExportedConfigBlob{Blob: blob}, nil
}

func pruneExportValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil:
		return nil, true
	case string:
		return v, v == ""
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			cleaned, empty := pruneExportValue(item)
			if !empty {
				out = append(out, cleaned)
			}
		}
		return out, false
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			cleaned, empty := pruneExportValue(item)
			if !empty {
				out[key] = cleaned
			}
		}
		return out, len(out) == 0
	default:
		return v, false
	}
}
