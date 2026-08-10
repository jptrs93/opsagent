package webuihandler

import (
	"cmp"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) GetV1GlobalState(ctx apigen.Context) (*apigen.GlobalState, error) {
	configs := h.Store.ListActiveDeploymentConfigs()
	configItems := make([]*apigen.DeploymentConfig, 0, len(configs))
	for _, cfg := range configs {
		configItems = append(configItems, redactDeploymentConfig(cfg))
	}
	slices.SortFunc(configItems, func(a, b *apigen.DeploymentConfig) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return &apigen.GlobalState{
		Spaces:            &apigen.SpaceList{Items: h.Store.ListSpaces()},
		Assets:            &apigen.AssetList{Items: h.Store.ListAssets()},
		Configs:           &apigen.ConfigList{Items: h.Store.ListConfigMetas()},
		Secrets:           &apigen.SecretList{Items: h.Store.ListSecretMetas()},
		DeploymentConfigs: &apigen.DeploymentConfigSnapshot{Items: configItems},
	}, nil
}

func (h *Handler) PostV1DeploymentsGet(ctx apigen.Context, req *apigen.DeploymentGetRequest) (*apigen.DeploymentState, error) {
	if req.ID <= 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.ID)
	if cfg == nil || cfg.Deleted {
		return nil, DeploymentNotFoundErr
	}
	states := make([]apigen.ScheduledInstanceState, 0, 2)
	for _, state := range h.Store.FetchScheduledSnapshotWithLatestFinal(nil) {
		if state.Instance.DeploymentID != req.ID {
			continue
		}
		states = append(states, state)
	}
	slices.SortFunc(states, func(a, b apigen.ScheduledInstanceState) int {
		return cmp.Compare(b.Instance.ID, a.Instance.ID)
	})
	instances := make([]*apigen.ScheduledInstanceState, 0, len(states))
	for i := range states {
		instances = append(instances, redactScheduledInstanceState(&states[i]))
	}
	return &apigen.DeploymentState{
		Config:    redactDeploymentConfig(cfg),
		Instances: &apigen.ScheduledInstanceSnapshot{Items: instances},
	}, nil
}
