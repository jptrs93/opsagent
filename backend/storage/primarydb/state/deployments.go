package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// MustFetchDeploymentHistory returns every event snapshot of a deployment in
// version order: spec updates, space moves, and the delete, each a full
// config at that point.
func (s *Service) MustFetchDeploymentHistory(deploymentID int32) []*apigen.Deployment {
	ctx := context.Background()
	events, err := s.q.ListDeploymentEvents(ctx, int64(deploymentID))
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentEvents: %v", err))
	}
	out := make([]*apigen.Deployment, 0, len(events))
	for _, e := range events {
		out = append(out, deploymentEventToProto(e))
	}
	return out
}

// DeploymentSpecUpdate is a full replacement of the spec, applied only when
// ExpectedSpecVersion matches the next spec version of the stored state.
// Callers always hold the current config (they need it for
// ExpectedSpecVersion), so unchanged fields are passed through as-is rather
// than merged from storage.
type DeploymentSpecUpdate struct {
	ExpectedSpecVersion int32
	Spec                *apigen.DeploymentSpec
}

func (s *Service) UpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, update DeploymentSpecUpdate) (*apigen.Deployment, bool, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	if update.Spec == nil {
		panic("deployment spec must not be nil")
	}
	existing := s.mustLatestDeploymentLocked(deploymentID, "deployment spec update")
	if update.ExpectedSpecVersion != existing.SpecVersion+1 {
		return existing, false, false
	}
	if deploymentSpecsEqual(update.Spec, &existing.Spec) {
		return existing, false, true
	}

	next := *existing
	next.Version = existing.Version + 1
	next.SpecVersion = existing.SpecVersion + 1
	next.UpdatedAt = time.Now()
	next.Author = int32(ctx.AttributionUserID())
	next.Spec = *update.Spec
	cfg := s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventUpdate, "deployment spec update")
	return cfg, true, true
}

// DeleteDeployment appends the tombstone event, guarded by the top-level
// version: expectedVersion must equal the current version + 1. The delete
// bumps only the top-level version — sub-part versions, the spec included,
// stay untouched, so the spec version remains strictly "times the spec
// changed".
func (s *Service) DeleteDeployment(ctx apigen.Context, deploymentID int32, expectedVersion int32) (*apigen.Deployment, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	existing := s.mustLatestDeploymentLocked(deploymentID, "deployment delete")
	if existing.Deleted || expectedVersion != existing.Version+1 {
		return existing, false
	}
	next := *existing
	next.Version = existing.Version + 1
	next.UpdatedAt = time.Now()
	next.Author = int32(ctx.AttributionUserID())
	next.Deleted = true
	cfg := s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventDelete, "deployment delete")
	return cfg, true
}

// mustAppendDeploymentEventLocked writes one event for the transition to next
// and refreshes the cache and subscribers. Caller must hold s.Mu and must
// have bumped next's versions to match the change it is making; the sub-
// version invariants are asserted here against the previous cached event.
func (s *Service) mustAppendDeploymentEventLocked(next *apigen.Deployment, eventType int64, label string) *apigen.Deployment {
	bgCtx := context.Background()
	prev, hasPrev := s.latestEvents[next.ID]
	event := buildDeploymentEvent(prev, hasPrev, next, eventType, label)
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(bgCtx)
		if err != nil {
			panic(fmt.Sprintf("NextGlobalSeq (%s): %v", label, err))
		}
		event.GlobalSeq = seq
		if err := q.InsertDeploymentEvent(bgCtx, event); err != nil {
			panic(fmt.Sprintf("InsertDeploymentEvent (%s): %v", label, err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s tx: %v", label, err))
	}
	return s.applyDeploymentEventLocked(event)
}

// mustLatestDeploymentLocked returns a fresh decode of the deployment's
// persisted latest event. Write paths base their next state on this rather
// than the cache: a caller holding a snapshot can alias the cached spec's
// pointer fields, and no-op/CAS decisions must rest on what is stored.
// Caller must hold s.Mu.
func (s *Service) mustLatestDeploymentLocked(deploymentID int32, label string) *apigen.Deployment {
	event, ok := s.latestEvents[deploymentID]
	if !ok {
		panic(fmt.Sprintf("%s: deployment %d has no events", label, deploymentID))
	}
	return deploymentEventToProto(event)
}

// applyDeploymentEventLocked installs a committed event into the caches and
// notifies subscribers. Caller must hold s.Mu.
func (s *Service) applyDeploymentEventLocked(event pq.DeploymentEvent) *apigen.Deployment {
	cfg := deploymentEventToProto(event)
	s.deploymentCache[cfg.ID] = cfg
	s.latestEvents[cfg.ID] = event
	s.notifyDeploymentLocked(cfg.ID)
	return cfg
}

// buildDeploymentEvent materialises next into an event row and asserts the
// versioning invariant the schema can no longer enforce structurally: the
// top-level version bumps by exactly one, and each sub-version increments iff
// its sub-value changed. GlobalSeq is left for the committing tx to fill.
func buildDeploymentEvent(prev pq.DeploymentEvent, hasPrev bool, next *apigen.Deployment, eventType int64, label string) pq.DeploymentEvent {
	if !hasPrev {
		if eventType != pq.DeploymentEventCreate || next.Version != 1 || next.SpecVersion != 1 || next.SpaceVersion != 1 {
			panic(fmt.Sprintf("%s: first event for deployment %d must be a v1 create", label, next.ID))
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
			panic(fmt.Sprintf("%s: deployment %d %s version %d does not match change (prev %d, changed %v)",
				label, next.ID, name, nextV, prevV, changed))
		}
	}
	if next.Version != prevCfg.Version+1 {
		panic(fmt.Sprintf("%s: deployment %d version %d does not follow %d", label, next.ID, next.Version, prevCfg.Version))
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

// mustAppendSpecVersionLocked appends the next spec version for an existing
// deployment, leaving identity fields untouched. Caller must hold s.Mu.
func (s *Service) mustAppendSpecVersionLocked(existing *apigen.Deployment, spec *apigen.DeploymentSpec, author int64, label string) *apigen.Deployment {
	next := *existing
	next.Version = existing.Version + 1
	next.SpecVersion = existing.SpecVersion + 1
	next.UpdatedAt = time.Now()
	next.Author = int32(author)
	next.Spec = *spec
	return s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventUpdate, label)
}

// mustCreateDeploymentLocked allocates the next deployment id and writes the
// create event, then caches and notifies. Caller must hold s.Mu.
func (s *Service) mustCreateDeploymentLocked(spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec, author int64, label string) *apigen.Deployment {
	bgCtx := context.Background()
	dbID, err := s.q.NextDeploymentID(bgCtx)
	if err != nil {
		panic(fmt.Sprintf("NextDeploymentID (%s): %v", label, err))
	}
	now := time.Now()
	next := apigen.Deployment{
		ID:           int32(dbID),
		NodeID:       nodeID,
		SpaceID:      spaceID,
		Version:      1,
		SpaceVersion: 1,
		Name:         name,
		CreatedAt:    now,
		UpdatedAt:    now,
		Author:       int32(author),
		SpecVersion:  1,
		Spec:         *spec,
	}
	return s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventCreate, label)
}

var DuplicateDeploymentIdentityErr = errors.New("a deployment with this name, space, and node already exists")

var SpaceVersionMismatchErr = errors.New("deployment space version mismatch")

// MoveDeploymentSpace appends a space-assignment event, guarded by the space
// version the caller observed: expectedSpaceVersion must equal the current
// space version + 1, mirroring the spec-update version guard. A same-space
// request with a valid guard is a no-op that does not advance the version.
func (s *Service) MoveDeploymentSpace(deploymentID, newSpaceID, expectedSpaceVersion, author int32) (*apigen.Deployment, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if _, ok := s.latestEvents[deploymentID]; !ok {
		return nil, fmt.Errorf("deployment %d not found", deploymentID)
	}
	cfg := s.mustLatestDeploymentLocked(deploymentID, "deployment space move")
	if cfg.Deleted {
		return nil, fmt.Errorf("deployment %d not found", deploymentID)
	}
	if expectedSpaceVersion != cfg.SpaceVersion+1 {
		return nil, SpaceVersionMismatchErr
	}
	if cfg.SpaceID == newSpaceID {
		cp := *cfg
		return &cp, nil
	}
	for _, other := range s.deploymentCache {
		if other.ID != deploymentID && !other.Deleted && storage.DeploymentKeyMatches(*other, cfg.NodeID, newSpaceID, cfg.Name) {
			return nil, DuplicateDeploymentIdentityErr
		}
	}
	next := *cfg
	next.Version = cfg.Version + 1
	next.SpaceID = newSpaceID
	next.SpaceVersion = cfg.SpaceVersion + 1
	next.UpdatedAt = time.Now()
	next.Author = author
	moved := s.mustAppendDeploymentEventLocked(&next, pq.DeploymentEventUpdate, "deployment space move")
	cp := *moved
	return &cp, nil
}

func (s *Service) MustCreateDeploymentForNode(ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.Deployment {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()

	for _, cfg := range s.deploymentCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, spaceID, name) && !cfg.Deleted {
			panic(fmt.Sprintf("deployment node=%d space=%d name=%q already exists", nodeID, spaceID, name))
		}
	}

	if spec == nil {
		panic("deployment spec must not be nil")
	}
	userID := int64(ctx.AttributionUserID())
	return s.mustCreateDeploymentLocked(spaceID, name, nodeID, spec, userID, "deployment")
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
				s.repairDeploymentSpecLocked(cfg.ID, internaldeploy.SelfSpec(), "system")
			}
			return
		}
	}

	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadState(opendeployVersion, true); err != nil {
		panic(fmt.Sprintf("initialize system deployment state: %v", err))
	}
	s.mustCreateDeploymentLocked(OpendeploySpaceID, internaldeploy.SelfName, nodeID, spec, 0, "system deployment")
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
				s.repairDeploymentSpecLocked(cfg.ID, desiredSpec, "netproxy")
				cfg = s.deploymentCache[cfg.ID]
			}
			return cfg
		}
	}

	spec := desiredSpec
	if err := spec.SetWorkloadState(desiredVersion, true); err != nil {
		panic(fmt.Sprintf("initialize netproxy deployment state: %v", err))
	}
	cfg := s.mustCreateDeploymentLocked(OpendeploySpaceID, internaldeploy.NetproxyName, nodeID, spec, 0, "netproxy deployment")
	slog.InfoContext(ctx, fmt.Sprintf("created netproxy deployment at version %s", desiredVersion), "node", nodeID)
	return cfg
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
	existing := s.mustLatestDeploymentLocked(deploymentID, label+" repair")
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve %s deployment workload state: %v", label, err))
	}
	s.mustAppendSpecVersionLocked(existing, storedSpec, 0, label+" deployment repair")
}
