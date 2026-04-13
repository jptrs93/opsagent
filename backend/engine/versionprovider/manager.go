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

type nudgeRequest struct {
	deploymentID int32
	scope        string
}

// Manager keeps an in-memory cache of scopes + versions for every deployment
// and periodically polls upstream (GitHub API, git ls-remote) for changes.
// Consumers subscribe to updates which are pushed via the state stream.
type Manager struct {
	source DeploymentSource

	mu    sync.Mutex
	cache map[int32]*apigen.DeploymentVersions // deployment_id -> versions

	subs    logstore.Subs[*apigen.DeploymentVersions]
	nudgeCh chan nudgeRequest
}

func NewManager(source DeploymentSource) *Manager {
	return &Manager{
		source:  source,
		cache:   make(map[int32]*apigen.DeploymentVersions),
		nudgeCh: make(chan nudgeRequest, 8),
	}
}

// Start begins the background polling loop. Call from handler init.
func (m *Manager) Start(ctx context.Context) {
	go m.pollLoop(ctx)
}

// Nudge triggers a targeted poll (non-blocking).
//   - deploymentID=0, scope="": poll all deployments (default scopes)
//   - deploymentID>0, scope="": poll all scopes for that deployment
//   - deploymentID>0, scope set: poll that specific scope for that deployment
func (m *Manager) Nudge(deploymentID int32, scope string) {
	select {
	case m.nudgeCh <- nudgeRequest{deploymentID: deploymentID, scope: scope}:
	default:
	}
}

// Snapshot returns the current cached versions for all deployments.
func (m *Manager) Snapshot() []*apigen.DeploymentVersions {
	m.mu.Lock()
	defer m.mu.Unlock()
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
		case req := <-m.nudgeCh:
			m.handleNudge(ctx, req)
		}
	}
}

func (m *Manager) handleNudge(ctx context.Context, req nudgeRequest) {
	if req.deploymentID == 0 {
		m.pollAll(ctx)
		return
	}

	dep := m.findDep(req.deploymentID)
	if dep == nil {
		return
	}

	if req.scope == "" {
		// Poll all scopes for this deployment.
		m.pollDeploymentAllScopes(ctx, dep)
	} else {
		// Poll a single specific scope.
		m.pollDeploymentScope(ctx, dep, req.scope)
	}
}

func (m *Manager) findDep(id int32) *apigen.DeploymentConfig {
	for _, dep := range m.source.ListActiveDeploymentConfigs() {
		if dep.ID == id {
			return dep
		}
	}
	return nil
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
		m.pollDeploymentDefaultScope(ctx, dep)
	}
}

// pollDeploymentDefaultScope polls scopes + versions for the default scope only.
// Used by the periodic poll to avoid hammering APIs.
func (m *Manager) pollDeploymentDefaultScope(ctx context.Context, dep *apigen.DeploymentConfig) {
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

	m.mergeAndNotify(dep.ID, scopes, versionsByScope)
}

// pollDeploymentAllScopes polls versions for every scope of a deployment.
func (m *Manager) pollDeploymentAllScopes(ctx context.Context, dep *apigen.DeploymentConfig) {
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
		vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, "")
		if err != nil {
			slog.Warn("version poll: listing versions failed", "deployment", dep.ID, "err", err)
			return
		}
		versionsByScope[""] = &apigen.ScopedVersions{Versions: vs}
	} else {
		for _, scope := range scopes {
			if ctx.Err() != nil {
				return
			}
			vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, scope)
			if err != nil {
				slog.Warn("version poll: listing versions failed", "deployment", dep.ID, "scope", scope, "err", err)
				continue
			}
			versionsByScope[scope] = &apigen.ScopedVersions{Versions: vs}
		}
	}

	m.mergeAndNotify(dep.ID, scopes, versionsByScope)
}

// pollDeploymentScope polls versions for a single scope and merges into the cache.
func (m *Manager) pollDeploymentScope(ctx context.Context, dep *apigen.DeploymentConfig, scope string) {
	provider, err := ForConfig(dep.Spec.Prepare)
	if err != nil {
		return
	}

	// Always refresh scopes too since we're being asked about this deployment.
	scopes, err := provider.ListScopes(ctx, dep.Spec.Prepare)
	if err != nil {
		slog.Warn("version poll: listing scopes failed", "deployment", dep.ID, "err", err)
		return
	}

	vs, err := provider.ListVersions(ctx, dep.Spec.Prepare, scope)
	if err != nil {
		slog.Warn("version poll: listing versions failed", "deployment", dep.ID, "scope", scope, "err", err)
		return
	}

	m.mergeAndNotify(dep.ID, scopes, map[string]*apigen.ScopedVersions{
		scope: {Versions: vs},
	})
}

// mergeAndNotify merges new scope/version data into the cache and notifies subscribers.
// Previously cached scopes that aren't in newVersionsByScope are preserved.
func (m *Manager) mergeAndNotify(depID int32, scopes []string, newVersionsByScope map[string]*apigen.ScopedVersions) {
	m.mu.Lock()
	existing := m.cache[depID]
	merged := make(map[string]*apigen.ScopedVersions)

	// Preserve previously cached scopes.
	if existing != nil {
		for k, v := range existing.VersionsByScope {
			merged[k] = v
		}
	}
	// Overlay new data.
	for k, v := range newVersionsByScope {
		merged[k] = v
	}

	entry := &apigen.DeploymentVersions{
		DeploymentID:    depID,
		Scopes:          scopes,
		VersionsByScope: merged,
	}
	m.cache[depID] = entry
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
