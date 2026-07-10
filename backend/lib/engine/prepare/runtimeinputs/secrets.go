package runtimeinputs

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SecretProvider interface {
	FetchSecrets(ctx context.Context, ids []int32) (map[int32]string, error)
}

type ConfigProvider interface {
	FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error)
}

type RuntimeInputs struct {
	assets  AssetProvider
	secrets SecretProvider
	configs ConfigProvider

	mu           sync.RWMutex
	secretValues map[int32]string
	configValues map[int32]string
}

func New(assets AssetProvider, secrets SecretProvider, configs ConfigProvider) *RuntimeInputs {
	return &RuntimeInputs{
		assets:       assets,
		secrets:      secrets,
		configs:      configs,
		secretValues: make(map[int32]string),
		configValues: make(map[int32]string),
	}
}

func (r *RuntimeInputs) EnsureSecretsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	ids := SecretRefs(cfg)
	if len(ids) == 0 {
		return nil
	}
	values, err := r.secrets.FetchSecrets(ctx, ids)
	if err != nil {
		return fmt.Errorf("fetching secrets: %w", err)
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("secret provider did not return id %d", id)
		}
	}
	r.mu.Lock()
	for _, id := range ids {
		r.secretValues[id] = values[id]
	}
	r.mu.Unlock()
	return nil
}

func (r *RuntimeInputs) EnsureReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	if err := r.EnsureAssetsReady(ctx, cfg); err != nil {
		return err
	}
	if err := r.EnsureSecretsReady(ctx, cfg); err != nil {
		return err
	}
	return r.EnsureConfigsReady(ctx, cfg)
}

func (r *RuntimeInputs) EnsureConfigsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	ids := ConfigRefs(cfg)
	if len(ids) == 0 {
		return nil
	}
	values, err := r.configs.FetchConfigs(ctx, ids)
	if err != nil {
		return fmt.Errorf("fetching configs: %w", err)
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("config provider did not return id %d", id)
		}
	}
	r.mu.Lock()
	for _, id := range ids {
		r.configValues[id] = values[id]
	}
	r.mu.Unlock()
	return nil
}

func (r *RuntimeInputs) ResolveSecret(id int32) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.secretValues[id]
	return value, ok
}

func (r *RuntimeInputs) ResolveConfig(id int32) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.configValues[id]
	return value, ok
}

func SecretRefs(cfg *apigen.DeploymentConfig) []int32 {
	if cfg == nil {
		return nil
	}
	seen := map[int32]bool{}
	for _, item := range cfg.Spec.Runner.Container.EnvVars {
		if item == nil || item.SecretID == nil || *item.SecretID == 0 {
			continue
		}
		seen[*item.SecretID] = true
	}
	ids := make([]int32, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func ConfigRefs(cfg *apigen.DeploymentConfig) []int32 {
	if cfg == nil {
		return nil
	}
	seen := map[int32]bool{}
	for _, item := range cfg.Spec.Runner.Container.EnvVars {
		if item == nil || item.ConfigID == nil || *item.ConfigID == 0 {
			continue
		}
		seen[*item.ConfigID] = true
	}
	ids := make([]int32, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
