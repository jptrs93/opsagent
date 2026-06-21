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
	keys := SecretRefs(cfg)
	if len(keys) == 0 {
		return nil
	}
	if Secrets == nil {
		return fmt.Errorf("secret provider is not configured")
	}
	values, err := Secrets.FetchSecrets(ctx, keys)
	if err != nil {
		return fmt.Errorf("fetching secrets: %w", err)
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return fmt.Errorf("secret provider did not return %q", key)
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
	keys := ConfigRefs(cfg)
	if len(keys) == 0 {
		return nil
	}
	if Configs == nil {
		return fmt.Errorf("config provider is not configured")
	}
	values, err := Configs.FetchConfigs(ctx, keys)
	if err != nil {
		return fmt.Errorf("fetching configs: %w", err)
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return fmt.Errorf("config provider did not return %q", key)
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
	if oo := cfg.Spec.Runner.Container.OpenobserveConsumer; oo != nil && oo.TokenSecretID != 0 {
		seen[oo.TokenSecretID] = true
		if oo.SaEmailValue != nil && oo.SaEmailValue.SecretID != nil && *oo.SaEmailValue.SecretID != 0 {
			seen[*oo.SaEmailValue.SecretID] = true
		}
	}

	keys := make([]int32, 0, len(seen))
	for id := range seen {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
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
	if oo := cfg.Spec.Runner.Container.OpenobserveConsumer; oo != nil && oo.SaEmailValue != nil && oo.SaEmailValue.ConfigID != nil && *oo.SaEmailValue.ConfigID != 0 {
		seen[*oo.SaEmailValue.ConfigID] = true
	}

	keys := make([]int32, 0, len(seen))
	for id := range seen {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
