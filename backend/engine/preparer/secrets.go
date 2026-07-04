package preparer

import (
	"context"
	"fmt"
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SecretProvider interface {
	FetchSecrets(ctx context.Context, ids []int32) (map[int32]string, error)
}

type ConfigProvider interface {
	FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error)
}

var Secrets SecretProvider
var Configs ConfigProvider

func EnsureSecretsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	ids := SecretRefs(cfg)
	if len(ids) == 0 {
		return nil
	}
	if Secrets == nil {
		return fmt.Errorf("secret provider is not configured")
	}
	values, err := Secrets.FetchSecrets(ctx, ids)
	if err != nil {
		return fmt.Errorf("fetching secrets: %w", err)
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("secret provider did not return id %d", id)
		}
	}
	return nil
}

func EnsureRuntimeInputsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	if err := EnsureAssetsReady(ctx, cfg); err != nil {
		return err
	}
	if err := EnsureSecretsReady(ctx, cfg); err != nil {
		return err
	}
	return EnsureConfigsReady(ctx, cfg)
}

func EnsureRuntimeRefsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	if err := EnsureSecretsReady(ctx, cfg); err != nil {
		return err
	}
	return EnsureConfigsReady(ctx, cfg)
}

func EnsureConfigsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	ids := ConfigRefs(cfg)
	if len(ids) == 0 {
		return nil
	}
	if Configs == nil {
		return fmt.Errorf("config provider is not configured")
	}
	values, err := Configs.FetchConfigs(ctx, ids)
	if err != nil {
		return fmt.Errorf("fetching configs: %w", err)
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("config provider did not return id %d", id)
		}
	}
	return nil
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
