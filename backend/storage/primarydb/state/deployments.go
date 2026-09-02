package state

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/sliceu"
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
		out = append(out, cfg)
	}
	return out
}

func (s *Service) MustFetchDeploymentHistory(ctx context.Context, deploymentID int32) []*apigen.Deployment {
	events := erru.Must(s.q.ListDeploymentEvents(ctx, int64(deploymentID)))
	return sliceu.Map(events, deploymentFromRow)
}

func (s *Service) CreateDeploymentLocked(ctx apigen.Context, def *apigen.DeploymentDef) *apigen.Deployment {
	dbID := erru.Must(s.q.NextDeploymentID(ctx))
	event := buildDeploymentEvent(nil, int32(dbID), def, ctx.AttributionUserID(), pq.DeploymentEventCreate)
	return s.mustAppendDeploymentEventLocked(ctx, event)
}

func (s *Service) UpdateDeploymentLocked(ctx apigen.Context, deploymentID int32, def *apigen.DeploymentDef) *apigen.Deployment {
	prev := s.mustLatestEventLocked(deploymentID)
	event := buildDeploymentEvent(&prev, deploymentID, def, ctx.AttributionUserID(), pq.DeploymentEventUpdate)
	return s.mustAppendDeploymentEventLocked(ctx, event)
}

func (s *Service) DeleteDeploymentLocked(ctx apigen.Context, deploymentID int32) *apigen.Deployment {
	prev := s.mustLatestEventLocked(deploymentID)
	def := erru.Must(apigen.DecodeDeploymentDef(prev.Value))
	event := buildDeploymentEvent(&prev, deploymentID, def, ctx.AttributionUserID(), pq.DeploymentEventDelete)
	return s.mustAppendDeploymentEventLocked(ctx, event)
}

func (s *Service) mustAppendDeploymentEventLocked(_ context.Context, event pq.DeploymentEvent) *apigen.Deployment {
	bgCtx := context.Background()
	s.q.TxMust(bgCtx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(bgCtx))
		event.GlobalSeq = seq
		return q.InsertDeploymentEvent(bgCtx, event)
	})
	cfg := deploymentFromRow(event)
	if event.EventType == pq.DeploymentEventDelete {
		delete(s.deploymentCache, cfg.ID)
		delete(s.latestEvents, cfg.ID)
	} else {
		s.deploymentCache[cfg.ID] = cfg
		s.latestEvents[cfg.ID] = event
	}
	s.notifyDeploymentLocked(*cfg)
	return cfg
}

func (s *Service) mustLatestEventLocked(deploymentID int32) pq.DeploymentEvent {
	event, ok := s.latestEvents[deploymentID]
	if !ok {
		panic(fmt.Sprintf("deployment %d is deleted or has no events", deploymentID))
	}
	return event
}

func buildDeploymentEvent(prev *pq.DeploymentEvent, deploymentID int32, updated *apigen.DeploymentDef, author int32, eventType int64) pq.DeploymentEvent {
	now := time.Now().UnixMilli()
	if eventType == pq.DeploymentEventCreate {
		return pq.DeploymentEvent{
			EventTime:              now,
			CreatedTime:            now,
			Author:                 int64(author),
			DeploymentID:           int64(deploymentID),
			Version:                1,
			SpecVersion:            1,
			SpaceAssignmentVersion: 1,
			NameVersion:            1,
			SpecChanged:            1,
			SpaceAssignmentChanged: 1,
			NameChanged:            1,
			Value:                  updated.Encode(),
			EventType:              pq.DeploymentEventCreate,
		}
	}
	prevDef := erru.Must(apigen.DecodeDeploymentDef(prev.Value))
	event := pq.DeploymentEvent{
		EventTime:              now,
		CreatedTime:            prev.CreatedTime,
		Author:                 int64(author),
		DeploymentID:           int64(deploymentID),
		Version:                prev.Version + 1,
		SpecVersion:            prev.SpecVersion,
		SpaceAssignmentVersion: prev.SpaceAssignmentVersion,
		NameVersion:            prev.NameVersion,
		Value:                  updated.Encode(),
		EventType:              eventType,
	}
	if !deploymentSpecsEqual(&updated.Spec, &prevDef.Spec) {
		event.SpecVersion++
		event.SpecChanged = 1
	}
	if updated.SpaceID != prevDef.SpaceID {
		event.SpaceAssignmentVersion++
		event.SpaceAssignmentChanged = 1
	}
	if updated.Name != prevDef.Name {
		event.NameVersion++
		event.NameChanged = 1
	}
	return event
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
		if storage.DeploymentKeyMatches(cfg.Def, nodeID, OpendeploySpaceID, internaldeploy.SelfName) {
			if !internaldeploy.IsSelfSpec(&cfg.Def.Spec) {
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
	s.CreateDeploymentLocked(apigen.Context{}, &apigen.DeploymentDef{
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
		if storage.DeploymentKeyMatches(cfg.Def, nodeID, OpendeploySpaceID, internaldeploy.NetproxyName) {
			if err := desiredSpec.SetWorkloadState(cfg.WorkloadVersion(), cfg.WorkloadRunning()); err != nil {
				panic(fmt.Sprintf("compare netproxy deployment state: %v", err))
			}
			if !deploymentSpecsEqual(&cfg.Def.Spec, desiredSpec) {
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
	cfg := s.CreateDeploymentLocked(apigen.Context{}, &apigen.DeploymentDef{
		NodeID:  nodeID,
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.NetproxyName,
		Spec:    *spec,
	})
	slog.InfoContext(ctx, fmt.Sprintf("created netproxy deployment at version %s", desiredVersion), "node", nodeID)
	return cfg
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec) {
	existing := deploymentFromRow(s.mustLatestEventLocked(deploymentID))
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment %d workload state: %v", deploymentID, err))
	}
	def := existing.Def
	def.Spec = *storedSpec
	s.UpdateDeploymentLocked(apigen.Context{}, deploymentID, &def)
}
