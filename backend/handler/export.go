package handler

import (
	"encoding/json"
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type exportedConfigBundle struct {
	Deployments []*apigen.DeploymentConfig   `json:"deployments"`
	Configs     []*apigen.UserConfig         `json:"configs"`
	Secrets     []*apigen.SecretMeta         `json:"secrets"`
	Assets      []*apigen.AssetMeta          `json:"assets"`
	Spaces      []*apigen.Space              `json:"spaces"`
	Settings    *apigen.DynamicConfiguration `json:"settings"`
}

func (h *Handler) PostV1GenerateExportedConfig(ctx apigen.Context, req *apigen.EmptyRequest) (*apigen.ExportedConfigBlob, error) {
	deployments := h.Store.ListActiveDeploymentConfigs()
	configs := h.Store.ListUserConfigs()
	secrets := h.listSecretMetas()
	assets := h.Store.ListAssets()
	spaces := h.Store.ListSpaces()
	settings := dynamicConfigToProto(h.ConfigService.Snapshot())

	sort.Slice(deployments, func(i, j int) bool { return deployments[i].ID < deployments[j].ID })
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
	sort.Slice(assets, func(i, j int) bool { return assets[i].Key < assets[j].Key })
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].ID < spaces[j].ID })

	blob, err := json.MarshalIndent(exportedConfigBundle{
		Deployments: deployments,
		Configs:     configs,
		Secrets:     secrets,
		Assets:      assets,
		Spaces:      spaces,
		Settings:    settings,
	}, "", "  ")
	if err != nil {
		return nil, err
	}

	return &apigen.ExportedConfigBlob{Blob: blob}, nil
}
