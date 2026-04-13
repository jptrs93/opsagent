package versionprovider

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logstore"
)

// DeploymentSource provides the current set of deployment configs so the
// manager knows what to poll. Satisfied by the storage adapter.
type DeploymentSource interface {
	ListActiveDeploymentConfigs() []*apigen.DeploymentConfig
}

// Manager keeps an in-memory cache of scopes + versions for every deployment
// and periodically polls upstream (GitHub API, git ls-remote) for changes.
// Consumers subscribe to updates which are pushed via the state stream.
type Manager struct {
	source DeploymentSource

	mu    sync.RWMutex
	cache map[int32]*apigen.DeploymentVersions // deployment_id -> versions

	subs   logstore.Subs[*apigen.DeploymentVersions]
	nudgeCh chan struct{}
}

func NewManager(source DeploymentSource) *Manager {
	return &Manager{
		source:  source,
		cache:   make(map[int32]*apigen.DeploymentVersions),
		nudgeCh: make(chan struct{}, 1),
	}
}

// Start begins the background polling loop. Call from handler init.
func (m *Manager) Start(ctx context.Context) {
	go m.pollLoop(ctx)
}

// Nudge triggers an immediate poll cycle (non-blocking).
func (m *Manager) Nudge() {
	select {
	case m.nudgeCh <- struct{}{}:
	default:
	}
}

// Snapshot returns the current cached versions for all deployments.
func (m *Manager) Snapshot() []*apigen.DeploymentVersions {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*apigen.DeploymentVersions, 0, len(m.cache))
	for _, v := range m.cache {
		out = append(out, v)
	}
	return out
}

// Subscribe returns a channel that receives per-deployment version updates.
func (m *Manager) Subscribe() (*logstore.Sub[*apigen.DeploymentVersions], func()) {
	return m.subs.Subscribe(nil)
}

func (m *Manager) pollLoop(ctx context.Context) {
	// Initial poll on startup.
	m.pollAll(ctx)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollAll(ctx)
		case <-m.nudgeCh:
			m.pollAll(ctx)
		}
	}
}

func (m *Manager) pollAll(ctx context.Context) {
	deps := m.source.ListActiveDeploymentConfigs()
	for _, dep := range deps {
		if ctx.Err() != nil {
			return
		}
		if dep.Spec == nil || dep.Spec.Prepare == nil {
			continue
		}
		m.pollDeployment(ctx, dep)
	}
}

func (m *Manager) pollDeployment(ctx context.Context, dep *apigen.DeploymentConfig) {
	provider, err := ForConfig(dep.Spec.Prepare)
	if err != nil {
		return
	}

	scopes, err := provider.ListScopes(ctx, dep.Spec.Prepare)
	if err != nil {
		slog.Warn("version poll: listing scopes failed", "deployment", dep.ID, "err", err)
		return
	}

	versionsByScope := make(map[string]*apigen.ScopedVersions)

	if len(scopes) == 0 {
		// GitHub releases: no scopes, single version list with empty-string key.
		vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, "")
		if err != nil {
			slog.Warn("version poll: listing versions failed", "deployment", dep.ID, "err", err)
			return
		}
		versionsByScope[""] = &apigen.ScopedVersions{Versions: vs}
	} else {
		// Nix builds: poll the default scope only to avoid hammering the API.
		// The full scope list is still sent so the FE can populate the dropdown.
		// When the user selects a non-default scope, the FE nudges + the existing
		// ListVersions endpoint can fill in the gap.
		defaultScope := "main"
		if !containsString(scopes, "main") && len(scopes) > 0 {
			defaultScope = scopes[0]
		}
		vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, defaultScope)
		if err != nil {
			slog.Warn("version poll: listing versions failed", "deployment", dep.ID, "scope", defaultScope, "err", err)
			return
		}
		versionsByScope[defaultScope] = &apigen.ScopedVersions{Versions: vs}
	}

	entry := &apigen.DeploymentVersions{
		DeploymentID:    dep.ID,
		Scopes:          scopes,
		VersionsByScope: versionsByScope,
	}

	m.mu.Lock()
	m.cache[dep.ID] = entry
	m.mu.Unlock()

	m.subs.Notify(entry)
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
