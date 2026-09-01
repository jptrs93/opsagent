package state

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func (s *Service) ListActiveDeployments() []*apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	out := make([]*apigen.Deployment, 0, len(s.deploymentCache))
	for _, cfg := range s.deploymentCache {
		if !cfg.Deleted {
			out = append(out, cfg)
		}
	}
	return out
}

func (s *Service) MustFetchDeploymentHistory(deploymentID int32) []*apigen.Deployment {
	ctx := context.Background()
	events := erru.Must(s.q.ListDeploymentEvents(ctx, int64(deploymentID)))
	out := make([]*apigen.Deployment, 0, len(events))
	for _, e := range events {
		out = append(out, deploymentEventToProto(e))
	}
	return out
}

func (s *Service) CreateDeploymentLocked(ctx apigen.Context, updated *apigen.Deployment) *apigen.Deployment {
	bgCtx := context.Background()
	dbID := erru.Must(s.q.NextDeploymentID(bgCtx))
	now := time.Now()
	next := *updated
	next.ID = int32(dbID)
	next.Version = 1
	next.SpecVersion = 1
	next.SpaceVersion = 1
	next.CreatedAt = now
	next.UpdatedAt = now
	next.Author = ctx.AttributionUserID()
	return s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventCreate)
}

type DeploymentUpdate struct {
	Spec    *apigen.DeploymentSpec
	SpaceID *int32
}

func (s *Service) UpdateDeploymentLocked(ctx apigen.Context, deploymentID int32, update DeploymentUpdate) *apigen.Deployment {
	existing := s.mustLatestDeploymentLocked(deploymentID)
	next := *existing
	changed := false
	if update.Spec != nil && !deploymentSpecsEqual(update.Spec, &existing.Spec) {
		next.Spec = *update.Spec
		next.SpecVersion = existing.SpecVersion + 1
		changed = true
	}
	if update.SpaceID != nil && *update.SpaceID != existing.SpaceID {
		next.SpaceID = *update.SpaceID
		next.SpaceVersion = existing.SpaceVersion + 1
		changed = true
	}
	if !changed {
		return existing
	}
	next.Version = existing.Version + 1
	next.UpdatedAt = time.Now()
	next.Author = ctx.AttributionUserID()
	return s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventUpdate)
}

func (s *Service) DeleteDeploymentLocked(ctx apigen.Context, deploymentID int32) *apigen.Deployment {
	existing := s.mustLatestDeploymentLocked(deploymentID)
	next := *existing
	next.Version = existing.Version + 1
	next.UpdatedAt = time.Now()
	next.Author = ctx.AttributionUserID()
	next.Deleted = true
	return s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventDelete)
}

func (s *Service) mustAppendDeploymentEventLocked(next *apigen.Deployment, eventType int64) *apigen.Deployment {
	bgCtx := context.Background()
	prev, hasPrev := s.latestEvents[next.ID]
	event := buildDeploymentEvent(prev, hasPrev, next, eventType)
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(bgCtx))
		event.GlobalSeq = seq
		return q.InsertDeploymentEvent(bgCtx, event)
	}); err != nil {
		panic(fmt.Sprintf("append deployment event tx: %v", err))
	}
	cfg := deploymentEventToProto(event)
	s.deploymentCache[cfg.ID] = cfg
	s.latestEvents[cfg.ID] = event
	s.notifyDeploymentLocked(cfg.ID)
	return cfg
}

func (s *Service) mustLatestDeploymentLocked(deploymentID int32) *apigen.Deployment {
	event, ok := s.latestEvents[deploymentID]
	if !ok {
		panic(fmt.Sprintf("deployment %d has no events", deploymentID))
	}
	return deploymentEventToProto(event)
}

func buildDeploymentEvent(prev pq.DeploymentEvent, hasPrev bool, next *apigen.Deployment, eventType int64) pq.DeploymentEvent {
	if !hasPrev {
		if eventType != pq.DeploymentEventCreate || next.Version != 1 || next.SpecVersion != 1 || next.SpaceVersion != 1 {
			panic(fmt.Sprintf("first event for deployment %d must be a v1 create", next.ID))
		}
		return pq.DeploymentEvent{
			CreatedAt:              next.UpdatedAt.UnixMilli(),
			Author:                 int64(next.Author),
			DeploymentID:           int64(next.ID),
			Version:                1,
			SpecVersion:            1,
			SpaceAssignmentVersion: 1,
			NameVersion:            1,
			Value:                  next.Encode(),
			EventType:              pq.DeploymentEventCreate,
		}
	}
	prevCfg := deploymentEventToProto(prev)
	assertSubVersion := func(name string, changed bool, prevV, nextV int32) {
		want := prevV
		if changed {
			want++
		}
		if nextV != want {
			panic(fmt.Sprintf("deployment %d %s version %d does not match change (prev %d, changed %v)",
				next.ID, name, nextV, prevV, changed))
		}
	}
	if next.Version != prevCfg.Version+1 {
		panic(fmt.Sprintf("deployment %d version %d does not follow %d", next.ID, next.Version, prevCfg.Version))
	}
	assertSubVersion("spec", !deploymentSpecsEqual(&next.Spec, &prevCfg.Spec), prevCfg.SpecVersion, next.SpecVersion)
	assertSubVersion("space", next.SpaceID != prevCfg.SpaceID, prevCfg.SpaceVersion, next.SpaceVersion)
	nameVersion := prev.NameVersion
	if next.Name != prevCfg.Name {
		nameVersion++
	}
	return pq.DeploymentEvent{
		CreatedAt:              next.UpdatedAt.UnixMilli(),
		Author:                 int64(next.Author),
		DeploymentID:           int64(next.ID),
		Version:                int64(next.Version),
		SpecVersion:            int64(next.SpecVersion),
		SpaceAssignmentVersion: int64(next.SpaceVersion),
		NameVersion:            nameVersion,
		Value:                  next.Encode(),
		EventType:              eventType,
	}
}

// EnsureSystemDeployment creates the OPENDEPLOY opendeploy deployment for
// the given node if it does not already exist. First-time system deployments
// are marked desired-running at opendeployVersion so the systemd runner can
// observe the already-running service.
func (s *Service) EnsureSystemDeployment(nodeID int32, opendeployVersion string) {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	if opendeployVersion == "" {
		panic("EnsureSystemDeployment requires an explicit OpenDeploy version")
	}

	s.Mu.Lock()
	defer s.Mu.Unlock()

	ctx := logu.AddTag(context.Background(), "Store")
	for _, cfg := range s.deploymentCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, OpendeploySpaceID, internaldeploy.SelfName) && !cfg.Deleted {
			if !internaldeploy.IsSelfSpec(&cfg.Spec) {
				slog.WarnContext(ctx, "repairing system deployment spec", "dep", cfg.ID, "node", nodeID)
				s.repairDeploymentSpecLocked(cfg.ID, internaldeploy.SelfSpec())
			}
			return
		}
	}

	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadState(opendeployVersion, true); err != nil {
		panic(fmt.Sprintf("initialize system deployment state: %v", err))
	}
	s.CreateDeploymentLocked(apigen.Context{}, &apigen.Deployment{
		NodeID:  nodeID,
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.SelfName,
		Spec:    *spec,
	})
	slog.InfoContext(ctx, fmt.Sprintf("created system deployment at version %s", opendeployVersion), "node", nodeID)
}

// EnsureNetproxyDeployment creates the per-node opendeploy-net internal
// deployment when missing. Existing desired state is administrator-managed.
func (s *Service) EnsureNetproxyDeployment(nodeID int32, initialVersion string) *apigen.Deployment {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	desiredVersion := strings.TrimSpace(initialVersion)
	if desiredVersion == "" {
		panic("EnsureNetproxyDeployment requires an explicit OpenDeploy version")
	}
	desiredSpec := internaldeploy.NetproxySpec()

	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	for _, cfg := range s.deploymentCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, OpendeploySpaceID, internaldeploy.NetproxyName) && !cfg.Deleted {
			if err := desiredSpec.SetWorkloadState(cfg.WorkloadVersion(), cfg.WorkloadRunning()); err != nil {
				panic(fmt.Sprintf("compare netproxy deployment state: %v", err))
			}
			if !deploymentSpecsEqual(&cfg.Spec, desiredSpec) {
				slog.WarnContext(ctx, "repairing netproxy deployment spec", "dep", cfg.ID, "node", nodeID)
				s.repairDeploymentSpecLocked(cfg.ID, desiredSpec)
				cfg = s.deploymentCache[cfg.ID]
			}
			return cfg
		}
	}

	spec := desiredSpec
	if err := spec.SetWorkloadState(desiredVersion, true); err != nil {
		panic(fmt.Sprintf("initialize netproxy deployment state: %v", err))
	}
	cfg := s.CreateDeploymentLocked(apigen.Context{}, &apigen.Deployment{
		NodeID:  nodeID,
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.NetproxyName,
		Spec:    *spec,
	})
	slog.InfoContext(ctx, fmt.Sprintf("created netproxy deployment at version %s", desiredVersion), "node", nodeID)
	return cfg
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec) {
	existing := s.mustLatestDeploymentLocked(deploymentID)
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment %d workload state: %v", deploymentID, err))
	}
	s.UpdateDeploymentLocked(apigen.Context{}, deploymentID, DeploymentUpdate{Spec: storedSpec})
}
