package state

import (
	"bytes"
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

func (s *Service) MustFetchDeploymentHistory(deploymentID int32) []*apigen.Deployment {
	ctx := context.Background()
	rows, err := s.q.ListDeploymentSpecVersions(ctx, int64(deploymentID))
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentSpecVersions: %v", err))
	}
	base := s.deploymentCache[deploymentID]
	out := make([]*apigen.Deployment, 0, len(rows))
	for _, r := range rows {
		out = append(out, specVersionRowToProto(r, base))
	}
	return out
}

// DeploymentSpecUpdate is a full replacement of the mutable config fields,
// applied only when ExpectedSpecVersion matches the next version of the stored row.
// Callers always hold the current row (they need it for ExpectedSpecVersion), so
// unchanged fields are passed through as-is rather than merged from storage.
type DeploymentSpecUpdate struct {
	ExpectedSpecVersion int32
	Spec                *apigen.DeploymentSpec
	Deleted             bool
}

func (s *Service) UpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, update DeploymentSpecUpdate) (*apigen.Deployment, bool, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	if update.Spec == nil {
		panic("deployment spec must not be nil")
	}

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	userID := int64(ctx.AttributionUserID())

	specBlob := update.Spec.Encode()

	existing, err := s.q.GetDeployment(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeployment: %v", err))
	}
	if update.ExpectedSpecVersion != int32(existing.SpecVersion+1) {
		return deploymentRowToProto(existing), false, false
	}

	next := existing
	if update.Deleted {
		if existing.DeletedAt == 0 {
			next.DeletedAt = now
		}
	} else {
		next.DeletedAt = 0
	}
	if bytes.Equal(specBlob, existing.SpecBlob) &&
		next.DeletedAt == existing.DeletedAt {
		return deploymentRowToProto(existing), false, true
	}

	// A delete appends a final version row even when the spec bytes are
	// unchanged — the tombstone's deletion time is its latest version's
	// created_at, and the bump keeps the optimistic version guard covering
	// deletes.
	next.SpecVersion = existing.SpecVersion + 1
	next.UpdatedAt = now
	next.Author = userID
	next.SpecBlob = specBlob
	cfg := s.mustCommitDeploymentLocked(existing, next, "deployment config update")
	return cfg, true, true
}

// mustCommitDeploymentLocked writes the version append and any identity
// changes between prev and next in one tx — the pair must never be observed
// half-applied — then refreshes the cache and notifies subscribers. Caller
// must hold s.Mu.
func (s *Service) mustCommitDeploymentLocked(prev, next pq.DeploymentRow, label string) *apigen.Deployment {
	bgCtx := context.Background()
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		if next.SpecVersion != prev.SpecVersion {
			seq, err := q.NextGlobalSeq(bgCtx)
			if err != nil {
				panic(fmt.Sprintf("NextGlobalSeq (%s): %v", label, err))
			}
			if err := q.InsertDeploymentSpecVersion(bgCtx, pq.InsertDeploymentSpecVersionParams{
				DeploymentID: next.DeploymentID,
				Version:      next.SpecVersion,
				CreatedAt:    next.UpdatedAt,
				Author:       next.Author,
				SpecBlob:     next.SpecBlob,
				GlobalSeq:    seq,
			}); err != nil {
				panic(fmt.Sprintf("InsertDeploymentSpecVersion (%s): %v", label, err))
			}
		}
		if next.DeletedAt != prev.DeletedAt {
			if err := q.UpdateDeploymentDeletedAt(bgCtx, pq.UpdateDeploymentDeletedAtParams{
				DeletedAt:    next.DeletedAt,
				DeploymentID: next.DeploymentID,
			}); err != nil {
				panic(fmt.Sprintf("UpdateDeploymentDeletedAt (%s): %v", label, err))
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s tx: %v", label, err))
	}
	cfg := deploymentRowToProto(next)
	id := int32(next.DeploymentID)
	s.deploymentCache[id] = cfg
	s.notifyDeploymentLocked(id)
	return cfg
}

// mustAppendSpecVersionLocked appends the next spec version for an existing
// deployment, leaving identity fields untouched. Caller must hold s.Mu.
func (s *Service) mustAppendSpecVersionLocked(existing pq.DeploymentRow, specBlob []byte, author int64, label string) *apigen.Deployment {
	next := existing
	next.SpecVersion = existing.SpecVersion + 1
	next.UpdatedAt = time.Now().UnixMilli()
	next.Author = author
	next.SpecBlob = specBlob
	return s.mustCommitDeploymentLocked(existing, next, label)
}

// mustCreateDeploymentLocked inserts a stable identity row and its v1 version
// row in one tx, then caches and notifies. Caller must hold s.Mu.
func (s *Service) mustCreateDeploymentLocked(spaceID int32, name string, nodeID int32, specBlob []byte, author int64, label string) *apigen.Deployment {
	bgCtx := context.Background()
	now := time.Now().UnixMilli()
	var dbID, spaceRowID int64
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(bgCtx)
		if err != nil {
			panic(fmt.Sprintf("NextGlobalSeq (%s): %v", label, err))
		}
		dbID, err = q.CreateDeployment(bgCtx, pq.CreateDeploymentParams{
			NodeID: int64(nodeID),
			Name:   name,
		})
		if err != nil {
			panic(fmt.Sprintf("CreateDeployment (%s): %v", label, err))
		}
		if err := q.InsertDeploymentSpecVersion(bgCtx, pq.InsertDeploymentSpecVersionParams{
			DeploymentID: dbID,
			Version:      1,
			CreatedAt:    now,
			Author:       author,
			SpecBlob:     specBlob,
			GlobalSeq:    seq,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentSpecVersion (%s): %v", label, err))
		}
		spaceRowID, err = q.InsertDeploymentSpaceVersion(bgCtx, pq.InsertDeploymentSpaceVersionParams{
			DeploymentID: dbID,
			Version:      1,
			Author:       author,
			CreatedAt:    now,
			SpaceID:      int64(spaceID),
			GlobalSeq:    seq,
		})
		if err != nil {
			panic(fmt.Sprintf("InsertDeploymentSpaceVersion (%s): %v", label, err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s create tx: %v", label, err))
	}

	cfg := deploymentRowToProto(pq.DeploymentRow{
		DeploymentID: dbID,
		NodeID:       int64(nodeID),
		SpaceID:      int64(spaceID),
		SpaceVersion: 1,
		Name:         name,
		SpecVersion:  1,
		CreatedAt:    now,
		UpdatedAt:    now,
		Author:       author,
		SpecBlob:     specBlob,
	})
	id := int32(dbID)
	s.deploymentCache[id] = cfg
	s.spaceVersionRowIDs[id] = spaceRowID
	s.notifyDeploymentLocked(id)
	return cfg
}

var DuplicateDeploymentIdentityErr = errors.New("a deployment with this name, space, and node already exists")

var SpaceVersionMismatchErr = errors.New("deployment space version mismatch")

// MoveDeploymentSpace appends a deployment_space_versions row, guarded by the
// space version the caller observed: expectedSpaceVersion must equal the
// current space version + 1, mirroring the config-update version guard. A
// same-space request with a valid guard is a no-op that does not advance the
// version.
func (s *Service) MoveDeploymentSpace(deploymentID, newSpaceID, expectedSpaceVersion, author int32) (*apigen.Deployment, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	cfg := s.deploymentCache[deploymentID]
	if cfg == nil || cfg.Deleted {
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
	ctx := context.Background()
	var rowID int64
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		rowID, err = q.InsertDeploymentSpaceVersion(ctx, pq.InsertDeploymentSpaceVersionParams{
			DeploymentID: int64(deploymentID),
			Version:      int64(cfg.SpaceVersion + 1),
			Author:       int64(author),
			CreatedAt:    time.Now().UnixMilli(),
			SpaceID:      int64(newSpaceID),
			GlobalSeq:    seq,
		})
		return err
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentSpaceVersion: %v", err))
	}
	next := *cfg
	next.SpaceID = newSpaceID
	next.SpaceVersion = cfg.SpaceVersion + 1
	s.deploymentCache[deploymentID] = &next
	s.spaceVersionRowIDs[deploymentID] = rowID
	s.notifyDeploymentLocked(deploymentID)
	cp := next
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
	return s.mustCreateDeploymentLocked(spaceID, name, nodeID, spec.Encode(), userID, "deployment")
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
	s.mustCreateDeploymentLocked(OpendeploySpaceID, internaldeploy.SelfName, nodeID, spec.Encode(), 0, "system deployment")
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
			if !bytes.Equal(cfg.Spec.Encode(), desiredSpec.Encode()) {
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
	cfg := s.mustCreateDeploymentLocked(OpendeploySpaceID, internaldeploy.NetproxyName, nodeID, spec.Encode(), 0, "netproxy deployment")
	slog.InfoContext(ctx, fmt.Sprintf("created netproxy deployment at version %s", desiredVersion), "node", nodeID)
	return cfg
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)

	existing, err := s.q.GetDeployment(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeployment (%s repair): %v", label, err))
	}
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.SpecVersion)
	existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.SpecVersion)
	if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve %s deployment workload state: %v", label, err))
	}
	s.mustAppendSpecVersionLocked(existing, storedSpec.Encode(), 0, label+" deployment repair")
}
