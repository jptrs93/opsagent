package webuihandler

import (
	"encoding/json"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type exportedConfigBundle struct {
	Deployments []*apigen.DeploymentRecord `json:"deployments"`
	Configs     []*pq.ConfigEvent          `json:"configs"`
	Secrets     []*pq.SecretEvent          `json:"secrets"`
	Assets      []*pq.AssetEvent           `json:"assets"`
	Spaces      []*apigen.Space            `json:"spaces"`
	Settings    apigen.ClusterSettings     `json:"settings"`
}

func (h *Handler) PostV1GlobalExportedConfig(ctx apigen.Context) (*apigen.ExportedConfigBlob, error) {
	// The export bundles every space's metadata plus cluster settings, so it is
	// gated on cluster-level view rather than filtered per space.
	if err := h.requireAccess(ctx, vView, eCluster, 0, 0); err != nil {
		return nil, err
	}
	deployments, err := h.Queries.ListActiveDeployments(ctx)
	if err != nil {
		return nil, err
	}
	if deployments == nil {
		deployments = []*apigen.DeploymentRecord{}
	}
	configs := values.ListConfigs(h.Store.Queries())
	secrets := secrets.List(h.Store.Queries())
	assets := assets.ListAssets(h.Store.Queries())
	spaces := nodes.ListSpaces(h.Store.Queries())
	storedSettings := h.SystemConfig.Snapshot().Settings
	settings := storedSettings

	sort.Slice(configs, func(i, j int) bool { return configs[i].Value.Fs.Key < configs[j].Value.Fs.Key })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Value.Fs.Key < secrets[j].Value.Fs.Key })
	sort.Slice(assets, func(i, j int) bool { return assets[i].Value.Fs.Key < assets[j].Value.Fs.Key })
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].ID < spaces[j].ID })

	content := exportedConfigBundle{
		Deployments: deployments,
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
