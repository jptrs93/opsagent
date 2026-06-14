package preparer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SecretProvider interface {
	FetchSecrets(ctx context.Context, keys []string) (map[string]string, error)
}

type ConfigProvider interface {
	FetchConfigs(ctx context.Context, keys []string) (map[string]string, error)
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

func SecretRefs(cfg *apigen.DeploymentConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	addEnvRefs := func(env []*apigen.EnvVar) {
		for _, item := range env {
			if item == nil {
				continue
			}
			for _, key := range refsInString(item.Value, "s") {
				seen[key] = true
			}
		}
	}
	addEnvRefs(cfg.Spec.Runner.OsProcess.Env)
	addEnvRefs(cfg.Spec.Runner.Container.Env)

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ConfigRefs(cfg *apigen.DeploymentConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	addEnvRefs := func(env []*apigen.EnvVar) {
		for _, item := range env {
			if item == nil {
				continue
			}
			for _, key := range refsInString(item.Value, "c") {
				seen[key] = true
			}
		}
	}
	addEnvRefs(cfg.Spec.Runner.OsProcess.Env)
	addEnvRefs(cfg.Spec.Runner.Container.Env)

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func refsInString(s, wantPrefix string) []string {
	if !strings.Contains(s, "${") {
		return nil
	}
	var refs []string
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			i++
			continue
		}
		switch s[i+1] {
		case '$':
			i += 2
			continue
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return refs
			}
			ref := strings.TrimSpace(s[i+2 : i+2+end])
			prefix, name, ok := strings.Cut(ref, ":")
			if ok && strings.TrimSpace(prefix) == wantPrefix {
				name = strings.TrimSpace(name)
				if name != "" {
					refs = append(refs, name)
				}
			}
			i += 2 + end + 1
			continue
		default:
			i++
		}
	}
	return refs
}
