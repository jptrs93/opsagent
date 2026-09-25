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
	FetchSecrets(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error)
}

type ConfigProvider interface {
	FetchConfigs(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error)
}

// Persistence durably stores fetched values so a node can resolve them again
// after a restart without reaching their provider. Optional: with no
// Persistence the values live in process memory only, which is what the primary
// wants — it already holds the authoritative copy.
type Persistence interface {
	LoadRuntimeInputs() (secrets, configs map[apigen.ValueRef]string, err error)
	StoreRuntimeInputs(secrets, configs map[apigen.ValueRef]string) error
	RetainRuntimeInputs(secrets, configs map[apigen.ValueRef]struct{}) (int, error)
}

type RuntimeInputs struct {
	assets      AssetProvider
	secrets     SecretProvider
	configs     ConfigProvider
	issuedTLS   IssuedTLSProvider
	persistence Persistence

	mu              sync.RWMutex
	secretValues    map[apigen.ValueRef]string
	configValues    map[apigen.ValueRef]string
	issuedTLSValues map[int32]*IssuedTLSValue
}

func New(assets AssetProvider, secrets SecretProvider, configs ConfigProvider) *RuntimeInputs {
	return &RuntimeInputs{
		assets:          assets,
		secrets:         secrets,
		configs:         configs,
		secretValues:    make(map[apigen.ValueRef]string),
		configValues:    make(map[apigen.ValueRef]string),
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
	for ref, value := range secretValues {
		r.secretValues[ref] = value
	}
	for ref, value := range configValues {
		r.configValues[ref] = value
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

// Retain drops every value, in memory and in persistence, whose ref is absent
// from the keep sets. It returns the number of persisted rows removed.
func (r *RuntimeInputs) Retain(secrets, configs map[apigen.ValueRef]struct{}) (int, error) {
	r.mu.Lock()
	for ref := range r.secretValues {
		if _, ok := secrets[ref]; !ok {
			delete(r.secretValues, ref)
		}
	}
	for ref := range r.configValues {
		if _, ok := configs[ref]; !ok {
			delete(r.configValues, ref)
		}
	}
	r.mu.Unlock()
	if r.persistence == nil {
		return 0, nil
	}
	return r.persistence.RetainRuntimeInputs(secrets, configs)
}

func (r *RuntimeInputs) missingRefs(refs []apigen.ValueRef, have map[apigen.ValueRef]string) []apigen.ValueRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]apigen.ValueRef, 0, len(refs))
	for _, ref := range refs {
		if _, ok := have[ref]; !ok {
			out = append(out, ref)
		}
	}
	return out
}

// persist writes through to durable storage, best effort.
//
// The values are already in memory and the deployment can run on them, so a
// local write failure must not fail preparation — it only costs a refetch on the
// next restart, which is exactly the behaviour of a node with no persistence.
func (r *RuntimeInputs) persist(ctx context.Context, secrets, configs map[apigen.ValueRef]string) {
	if r.persistence == nil {
		return
	}
	if err := r.persistence.StoreRuntimeInputs(secrets, configs); err != nil {
		slog.WarnContext(ctx, "runtimeinputs: persisting values locally failed; they will be refetched after a restart", "err", err)
	}
}

// EnsureSecretsReady makes every secret referenced by cfg resolvable on this
// node, fetching only the refs not already held.
//
// Skipping refs already held is safe because secret values are immutable: a
// (secret id, value version) pair always denotes the same value, and rotation
// mints a new value version that arrives here as a new deployment spec version. Combined with Persistence this is what
// lets a restarted secondary start its workloads without reaching the primary at
// all.
func (r *RuntimeInputs) EnsureSecretsReady(ctx context.Context, cfg *apigen.DeploymentEvent) error {
	return r.EnsureSecretRefs(ctx, SecretRefs(cfg))
}

// EnsureSecretRefs makes the given secret values resolvable on this node,
// fetching only the refs not already held.
func (r *RuntimeInputs) EnsureSecretRefs(ctx context.Context, refs []apigen.ValueRef) error {
	if len(refs) == 0 {
		return nil
	}
	missing := r.missingRefs(refs, r.secretValues)
	if len(missing) == 0 {
		return nil
	}
	values, err := r.secrets.FetchSecrets(ctx, missing)
	if err != nil {
		return fmt.Errorf("fetching secrets: %w", err)
	}
	for _, ref := range missing {
		if _, ok := values[ref]; !ok {
			return fmt.Errorf("secret provider did not return secret %s", ref)
		}
	}
	fetched := make(map[apigen.ValueRef]string, len(missing))
	r.mu.Lock()
	for _, ref := range missing {
		r.secretValues[ref] = values[ref]
		fetched[ref] = values[ref]
	}
	r.mu.Unlock()
	r.persist(ctx, fetched, nil)
	return nil
}

func (r *RuntimeInputs) EnsureReady(ctx context.Context, cfg *apigen.DeploymentEvent) error {
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
func (r *RuntimeInputs) EnsureConfigsReady(ctx context.Context, cfg *apigen.DeploymentEvent) error {
	refs := ConfigRefs(cfg)
	if len(refs) == 0 {
		return nil
	}
	missing := r.missingRefs(refs, r.configValues)
	if len(missing) == 0 {
		return nil
	}
	values, err := r.configs.FetchConfigs(ctx, missing)
	if err != nil {
		return fmt.Errorf("fetching configs: %w", err)
	}
	for _, ref := range missing {
		if _, ok := values[ref]; !ok {
			return fmt.Errorf("config provider did not return config %s", ref)
		}
	}
	fetched := make(map[apigen.ValueRef]string, len(missing))
	r.mu.Lock()
	for _, ref := range missing {
		r.configValues[ref] = values[ref]
		fetched[ref] = values[ref]
	}
	r.mu.Unlock()
	r.persist(ctx, nil, fetched)
	return nil
}

func (r *RuntimeInputs) ResolveSecret(ref apigen.ValueRef) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.secretValues[ref]
	return value, ok
}

func (r *RuntimeInputs) ResolveConfig(ref apigen.ValueRef) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.configValues[ref]
	return value, ok
}

func SecretRefs(cfg *apigen.DeploymentEvent) []apigen.ValueRef {
	if cfg == nil {
		return nil
	}
	seen := map[apigen.ValueRef]bool{}
	if container := cfg.Value.Spec.Container(); container != nil {
		for _, item := range container.Runtime.EnvVars {
			if item == nil || item.Secret == nil || !item.Secret.Valid() {
				continue
			}
			seen[*item.Secret] = true
		}
	}
	for _, route := range cfg.Value.Spec.Networking.Ingress {
		if route == nil || route.HttpsConfig == nil || route.HttpsConfig.CertSource == nil {
			continue
		}
		if secret := route.HttpsConfig.CertSource.Secret; secret != nil && secret.Secret.Valid() {
			seen[secret.Secret] = true
		}
	}
	return sortedRefs(seen)
}

func ConfigRefs(cfg *apigen.DeploymentEvent) []apigen.ValueRef {
	if cfg == nil {
		return nil
	}
	container := cfg.Value.Spec.Container()
	if container == nil {
		return nil
	}
	seen := map[apigen.ValueRef]bool{}
	for _, item := range container.Runtime.EnvVars {
		if item == nil || item.Config == nil || !item.Config.Valid() {
			continue
		}
		seen[*item.Config] = true
	}
	return sortedRefs(seen)
}

func sortedRefs(seen map[apigen.ValueRef]bool) []apigen.ValueRef {
	refs := make([]apigen.ValueRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Less(refs[j]) })
	return refs
}
