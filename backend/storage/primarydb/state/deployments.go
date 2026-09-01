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
		if !cfg.Deleted() {
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
		out = append(out, deploymentFromRow(e))
	}
	return out
}

func (s *Service) CreateDeploymentLocked(ctx apigen.Context, def *apigen.DeploymentDef) *apigen.Deployment {
	bgCtx := context.Background()
	dbID := erru.Must(s.q.NextDeploymentID(bgCtx))
	return s.mustAppendDeploymentEventLocked(int32(dbID), def, ctx.AttributionUserID(), pq.DeploymentEventCreate)
}

type DeploymentUpdate struct {
	Spec    *apigen.DeploymentSpec
	SpaceID *int32
}

func (s *Service) UpdateDeploymentLocked(ctx apigen.Context, deploymentID int32, update DeploymentUpdate) *apigen.Deployment {
	existing := s.mustLatestDeploymentLocked(deploymentID)
	def := existing.Def
	changed := false
	if update.Spec != nil && !deploymentSpecsEqual(update.Spec, &def.Spec) {
		def.Spec = *update.Spec
		changed = true
	}
	if update.SpaceID != nil && *update.SpaceID != def.SpaceID {
		def.SpaceID = *update.SpaceID
		changed = true
	}
	if !changed {
		return existing
	}
	return s.mustAppendDeploymentEventLocked(deploymentID, &def, ctx.AttributionUserID(), pq.DeploymentEventUpdate)
}

func (s *Service) DeleteDeploymentLocked(ctx apigen.Context, deploymentID int32) *apigen.Deployment {
	existing := s.mustLatestDeploymentLocked(deploymentID)
	def := existing.Def
	return s.mustAppendDeploymentEventLocked(deploymentID, &def, ctx.AttributionUserID(), pq.DeploymentEventDelete)
}

func (s *Service) mustAppendDeploymentEventLocked(deploymentID int32, def *apigen.DeploymentDef, author int32, eventType int64) *apigen.Deployment {
	bgCtx := context.Background()
	prev, hasPrev := s.latestEvents[deploymentID]
	event := buildDeploymentEvent(prev, hasPrev, deploymentID, def, author, eventType)
	s.q.TxMust(bgCtx, func(q *pq.Queries) error {
		seq := erru.Must(q.NextGlobalSeq(bgCtx))
		event.GlobalSeq = seq
		return q.InsertDeploymentEvent(bgCtx, event)
	})
	cfg := deploymentFromRow(event)
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
	return deploymentFromRow(event)
}

func buildDeploymentEvent(prev pq.DeploymentEvent, hasPrev bool, deploymentID int32, def *apigen.DeploymentDef, author int32, eventType int64) pq.DeploymentEvent {
	now := time.Now().UnixMilli()
	if !hasPrev {
		if eventType != pq.DeploymentEventCreate {
			panic(fmt.Sprintf("first event for deployment %d must be a create", deploymentID))
		}
		return pq.DeploymentEvent{
			EventTime:              now,
			CreatedTime:            now,
			Author:                 int64(author),
			DeploymentID:           int64(deploymentID),
			Version:                1,
			SpecVersion:            1,
			SpaceAssignmentVersion: 1,
			NameVersion:            1,
			Value:                  def.Encode(),
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
		Value:                  def.Encode(),
		EventType:              eventType,
	}
	if !deploymentSpecsEqual(&def.Spec, &prevDef.Spec) {
		event.SpecVersion++
	}
	if def.SpaceID != prevDef.SpaceID {
		event.SpaceAssignmentVersion++
	}
	if def.Name != prevDef.Name {
		event.NameVersion++
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
		if storage.DeploymentKeyMatches(cfg.Def, nodeID, OpendeploySpaceID, internaldeploy.SelfName) && !cfg.Deleted() {
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
		if storage.DeploymentKeyMatches(cfg.Def, nodeID, OpendeploySpaceID, internaldeploy.NetproxyName) && !cfg.Deleted() {
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
	existing := s.mustLatestDeploymentLocked(deploymentID)
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment %d workload state: %v", deploymentID, err))
	}
	s.UpdateDeploymentLocked(apigen.Context{}, deploymentID, DeploymentUpdate{Spec: storedSpec})
}
