package runtimeinputs

import (
	"context"
	"fmt"
	"log/slog"
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

// Persistence durably stores fetched values so a node can resolve them again
// after a restart without reaching their provider. Optional: with no
// Persistence the values live in process memory only, which is what the primary
// wants — it already holds the authoritative copy.
type Persistence interface {
	LoadRuntimeInputs() (secrets, configs map[int32]string, err error)
	StoreRuntimeInputs(secrets, configs map[int32]string) error
	RetainRuntimeInputs(secrets, configs map[int32]struct{}) (int, error)
}

type RuntimeInputs struct {
	assets      AssetProvider
	secrets     SecretProvider
	configs     ConfigProvider
	issuedTLS   IssuedTLSProvider
	persistence Persistence

	mu              sync.RWMutex
	secretValues    map[int32]string
	configValues    map[int32]string
	issuedTLSValues map[int32]*IssuedTLSValue
}

func New(assets AssetProvider, secrets SecretProvider, configs ConfigProvider) *RuntimeInputs {
	return &RuntimeInputs{
		assets:          assets,
		secrets:         secrets,
		configs:         configs,
		secretValues:    make(map[int32]string),
		configValues:    make(map[int32]string),
		issuedTLSValues: make(map[int32]*IssuedTLSValue),
	}
}

// NewPersistent returns a RuntimeInputs backed by p, preloaded with everything p
// already holds.
//
// On error the returned RuntimeInputs is still usable — it just starts empty and
// refetches — so a caller that only logs the error stays correct.
func NewPersistent(assets AssetProvider, secrets SecretProvider, configs ConfigProvider, p Persistence) (*RuntimeInputs, error) {
	r := New(assets, secrets, configs)
	if p == nil {
		return r, nil
	}
	r.persistence = p
	secretValues, configValues, err := p.LoadRuntimeInputs()
	if err != nil {
		return r, fmt.Errorf("loading persisted runtime inputs: %w", err)
	}
	r.mu.Lock()
	for id, value := range secretValues {
		r.secretValues[id] = value
	}
	for id, value := range configValues {
		r.configValues[id] = value
	}
	r.mu.Unlock()
	if tp, ok := p.(IssuedTLSPersistence); ok {
		issued, err := tp.LoadIssuedTLS()
		if err != nil {
			return r, fmt.Errorf("loading persisted issued TLS: %w", err)
		}
		r.mu.Lock()
		for id, value := range issued {
			r.issuedTLSValues[id] = value
		}
		r.mu.Unlock()
	}
	return r, nil
}

// Retain drops every value, in memory and in persistence, whose id is absent
// from the keep sets. It returns the number of persisted rows removed.
func (r *RuntimeInputs) Retain(secrets, configs map[int32]struct{}) (int, error) {
	r.mu.Lock()
	for id := range r.secretValues {
		if _, ok := secrets[id]; !ok {
			delete(r.secretValues, id)
		}
	}
	for id := range r.configValues {
		if _, ok := configs[id]; !ok {
			delete(r.configValues, id)
		}
	}
	r.mu.Unlock()
	if r.persistence == nil {
		return 0, nil
	}
	return r.persistence.RetainRuntimeInputs(secrets, configs)
}

// missingIDs returns the subset of ids with no value held yet.
func (r *RuntimeInputs) missingIDs(ids []int32, have map[int32]string) []int32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if _, ok := have[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// persist writes through to durable storage, best effort.
//
// The values are already in memory and the deployment can run on them, so a
// local write failure must not fail preparation — it only costs a refetch on the
// next restart, which is exactly the behaviour of a node with no persistence.
func (r *RuntimeInputs) persist(ctx context.Context, secrets, configs map[int32]string) {
	if r.persistence == nil {
		return
	}
	if err := r.persistence.StoreRuntimeInputs(secrets, configs); err != nil {
		slog.WarnContext(ctx, "runtimeinputs: persisting values locally failed; they will be refetched after a restart", "err", err)
	}
}

// EnsureSecretsReady makes every secret referenced by cfg resolvable on this
// node, fetching only the ids not already held.
//
// Skipping ids already held is safe because secret rows are immutable: an id
// always denotes the same value, and rotation mints a new id that arrives here
// as a new deployment config version. Combined with Persistence this is what
// lets a restarted worker start its workloads without reaching the primary at
// all.
func (r *RuntimeInputs) EnsureSecretsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	return r.EnsureSecretIDs(ctx, SecretRefs(cfg))
}

// EnsureSecretIDs makes the given secret version ids resolvable on this node,
// fetching only the ids not already held.
func (r *RuntimeInputs) EnsureSecretIDs(ctx context.Context, ids []int32) error {
	if len(ids) == 0 {
		return nil
	}
	missing := r.missingIDs(ids, r.secretValues)
	if len(missing) == 0 {
		return nil
	}
	values, err := r.secrets.FetchSecrets(ctx, missing)
	if err != nil {
		return fmt.Errorf("fetching secrets: %w", err)
	}
	for _, id := range missing {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("secret provider did not return id %d", id)
		}
	}
	fetched := make(map[int32]string, len(missing))
	r.mu.Lock()
	for _, id := range missing {
		r.secretValues[id] = values[id]
		fetched[id] = values[id]
	}
	r.mu.Unlock()
	r.persist(ctx, fetched, nil)
	return nil
}

func (r *RuntimeInputs) EnsureReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	if err := r.EnsureAssetsReady(ctx, cfg); err != nil {
		return err
	}
	if err := r.EnsureSecretsReady(ctx, cfg); err != nil {
		return err
	}
	if err := r.EnsureConfigsReady(ctx, cfg); err != nil {
		return err
	}
	return r.EnsureIssuedTLSReady(ctx, cfg)
}

// EnsureConfigsReady is EnsureSecretsReady for plain config values, which share
// the same immutable-versioned row model.
func (r *RuntimeInputs) EnsureConfigsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	ids := ConfigRefs(cfg)
	if len(ids) == 0 {
		return nil
	}
	missing := r.missingIDs(ids, r.configValues)
	if len(missing) == 0 {
		return nil
	}
	values, err := r.configs.FetchConfigs(ctx, missing)
	if err != nil {
		return fmt.Errorf("fetching configs: %w", err)
	}
	for _, id := range missing {
		if _, ok := values[id]; !ok {
			return fmt.Errorf("config provider did not return id %d", id)
		}
	}
	fetched := make(map[int32]string, len(missing))
	r.mu.Lock()
	for _, id := range missing {
		r.configValues[id] = values[id]
		fetched[id] = values[id]
	}
	r.mu.Unlock()
	r.persist(ctx, nil, fetched)
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
	if container := cfg.Spec.Container(); container != nil {
		for _, item := range container.Runtime.EnvVars {
			if item == nil || item.SecretVersionID == nil || *item.SecretVersionID == 0 {
				continue
			}
			seen[*item.SecretVersionID] = true
		}
	}
	for _, route := range cfg.Spec.Networking.Ingress {
		if route == nil || route.HttpsConfig == nil || route.HttpsConfig.CertSource == nil {
			continue
		}
		if secret := route.HttpsConfig.CertSource.Secret; secret != nil && secret.SecretVersionID > 0 {
			seen[secret.SecretVersionID] = true
		}
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
	container := cfg.Spec.Container()
	if container == nil {
		return nil
	}
	seen := map[int32]bool{}
	for _, item := range container.Runtime.EnvVars {
		if item == nil || item.ConfigVersionID == nil || *item.ConfigVersionID == 0 {
			continue
		}
		seen[*item.ConfigVersionID] = true
	}
	ids := make([]int32, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
