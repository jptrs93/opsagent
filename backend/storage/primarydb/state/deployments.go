package state

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// ListActiveDeploymentConfigs returns all non-deleted configs from the cache.
func (s *Service) ListActiveDeploymentConfigs() []*apigen.DeploymentConfig {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	out := make([]*apigen.DeploymentConfig, 0, len(s.configCache))
	for _, cfg := range s.configCache {
		if !cfg.Deleted {
			out = append(out, cfg)
		}
	}
	return out
}

// --- deployment history ---

func (s *Service) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
	ctx := context.Background()
	rows, err := s.q.ListDeploymentVersions(ctx, int64(deploymentID))
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentVersions: %v", err))
	}
	base := s.configCache[deploymentID]
	out := make([]*apigen.DeploymentConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, configVersionRowToProto(r, base))
	}
	return out
}

// --- deployment config update ---

// DeploymentConfigUpdate is a full replacement of the mutable config fields,
// applied only when ExpectedVersion matches the next version of the stored row.
// Callers always hold the current row (they need it for ExpectedVersion), so
// unchanged fields are passed through as-is rather than merged from storage.
// Space is not updatable: moves are rejected at the API until the planned
// deployment_spaces entity captures space placements with its own versioning.
type DeploymentConfigUpdate struct {
	ExpectedVersion int32
	Spec            *apigen.DeploymentSpec
	Deleted         bool
}

func (s *Service) UpdateDeploymentConfig(ctx apigen.Context, deploymentID int32, update DeploymentConfigUpdate) (*apigen.DeploymentConfig, bool, bool) {
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

	existing, err := s.q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}
	if update.ExpectedVersion != int32(existing.Version+1) {
		return configRowToProto(existing), false, false
	}

	next := existing
	next.Deleted = boolToInt(update.Deleted)
	if bytes.Equal(specBlob, existing.SpecBlob) &&
		next.Deleted == existing.Deleted {
		return configRowToProto(existing), false, true
	}

	// A delete appends a final version row even when the spec bytes are
	// unchanged — the tombstone's deletion time is its latest version's
	// created_at, and the bump keeps the optimistic version guard covering
	// deletes.
	next.Version = existing.Version + 1
	next.UpdatedAt = now
	next.Author = userID
	next.SpecBlob = specBlob
	cfg := s.mustCommitDeploymentConfigLocked(existing, next, "deployment config update")
	return cfg, true, true
}

// mustCommitDeploymentConfigLocked writes the version append and any identity
// changes between prev and next in one tx — the pair must never be observed
// half-applied — then refreshes the cache and notifies subscribers. Caller
// must hold s.Mu.
func (s *Service) mustCommitDeploymentConfigLocked(prev, next pq.DeploymentConfigRow, label string) *apigen.DeploymentConfig {
	bgCtx := context.Background()
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		if next.Version != prev.Version {
			if err := q.InsertDeploymentVersion(bgCtx, pq.InsertDeploymentVersionParams{
				DeploymentID: next.DeploymentID,
				Version:      next.Version,
				CreatedAt:    next.UpdatedAt,
				Author:       next.Author,
				SpecBlob:     next.SpecBlob,
			}); err != nil {
				panic(fmt.Sprintf("InsertDeploymentVersion (%s): %v", label, err))
			}
		}
		if next.Deleted != prev.Deleted {
			if err := q.UpdateDeploymentConfigDeleted(bgCtx, pq.UpdateDeploymentConfigDeletedParams{
				Deleted:      next.Deleted,
				DeploymentID: next.DeploymentID,
			}); err != nil {
				panic(fmt.Sprintf("UpdateDeploymentConfigDeleted (%s): %v", label, err))
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s tx: %v", label, err))
	}
	cfg := configRowToProto(next)
	id := int32(next.DeploymentID)
	s.configCache[id] = cfg
	s.notifyConfigLocked(id)
	return cfg
}

// mustAppendConfigVersionLocked appends the next spec version for an existing
// deployment, leaving identity fields untouched. Caller must hold s.Mu.
func (s *Service) mustAppendConfigVersionLocked(existing pq.DeploymentConfigRow, specBlob []byte, author int64, label string) *apigen.DeploymentConfig {
	next := existing
	next.Version = existing.Version + 1
	next.UpdatedAt = time.Now().UnixMilli()
	next.Author = author
	next.SpecBlob = specBlob
	return s.mustCommitDeploymentConfigLocked(existing, next, label)
}

func (s *Service) MustSetDeploymentWorkloadState(ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.mustSetDeploymentWorkloadStateLocked(ctx, deploymentID, version, running)
}

func (s *Service) mustSetDeploymentWorkloadStateLocked(ctx apigen.Context, deploymentID int32, version string, running bool) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)

	userID := int64(ctx.AttributionUserID())

	existing, err := s.q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}
	spec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(fmt.Sprintf("update deployment workload state: %v", err))
	}
	s.mustAppendConfigVersionLocked(existing, spec.Encode(), userID, "deployment workload state")
}

// --- deployment spec update ---

func (s *Service) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)

	userID := int64(ctx.AttributionUserID())

	if spec == nil {
		panic("deployment spec must not be nil")
	}

	existing, err := s.q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.Version)
	existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment workload state: %v", err))
	}
	s.mustAppendConfigVersionLocked(existing, storedSpec.Encode(), userID, "deployment spec update")
}

// mustCreateDeploymentLocked inserts a stable identity row and its v1 version
// row in one tx, then caches and notifies. Caller must hold s.Mu.
func (s *Service) mustCreateDeploymentLocked(cid *apigen.DeploymentIdentity, nodeID int32, specBlob []byte, author int64, label string) *apigen.DeploymentConfig {
	bgCtx := context.Background()
	now := time.Now().UnixMilli()
	var dbID int64
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		var err error
		dbID, err = q.CreateDeploymentConfig(bgCtx, pq.CreateDeploymentConfigParams{
			NodeID:  int64(nodeID),
			SpaceID: int64(cid.SpaceID),
			Name:    cid.Name,
		})
		if err != nil {
			panic(fmt.Sprintf("CreateDeploymentConfig (%s): %v", label, err))
		}
		if err := q.InsertDeploymentVersion(bgCtx, pq.InsertDeploymentVersionParams{
			DeploymentID: dbID,
			Version:      1,
			CreatedAt:    now,
			Author:       author,
			SpecBlob:     specBlob,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentVersion (%s): %v", label, err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s create tx: %v", label, err))
	}

	cfg := configRowToProto(pq.DeploymentConfigRow{
		DeploymentID: dbID,
		NodeID:       int64(nodeID),
		SpaceID:      int64(cid.SpaceID),
		Name:         cid.Name,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
		Author:       author,
		SpecBlob:     specBlob,
	})
	id := int32(dbID)
	s.configCache[id] = cfg
	s.notifyConfigLocked(id)
	return cfg
}

// MustCreateDeploymentForNode creates a deployment with an explicit canonical
// node assignment.
func (s *Service) MustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	return s.mustCreateDeploymentForNode(ctx, cid, nodeID, spec)
}

func (s *Service) mustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	// Reject if a non-deleted deployment with the same semantic key already exists.
	for _, cfg := range s.configCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, *cid) && !cfg.Deleted {
			panic(fmt.Sprintf("deployment node=%d space=%d name=%q already exists", nodeID, cid.SpaceID, cid.Name))
		}
	}

	if spec == nil {
		panic("deployment spec must not be nil")
	}
	userID := int64(ctx.AttributionUserID())
	return s.mustCreateDeploymentLocked(cid, nodeID, spec.Encode(), userID, "deployment")
}

// EnsureSystemDeployment creates the OPENDEPLOY opendeploy deployment for
// the given node if it does not already exist. When opendeployVersion is
// known, first-time system deployments are marked desired-running at that
// version so the systemd runner can observe the already-running service.
func (s *Service) EnsureSystemDeployment(nodeID int32, opendeployVersion string) {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	cid := apigen.DeploymentIdentity{
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.SelfName,
	}

	s.Mu.Lock()
	defer s.Mu.Unlock()

	// Check if it already exists.
	for _, cfg := range s.configCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, cid) && !cfg.Deleted {
			if !internaldeploy.IsSelfSpec(&cfg.Spec) {
				slog.Warn("repairing system deployment spec", "nodeID", nodeID, "deploymentID", cfg.ID)
				s.repairDeploymentSpecLocked(cfg.ID, internaldeploy.SelfSpec(), "system")
			}
			return
		}
	}

	spec := internaldeploy.SelfSpec()
	if err := spec.SetWorkloadState(opendeployVersion, opendeployVersion != ""); err != nil {
		panic(fmt.Sprintf("initialize system deployment state: %v", err))
	}
	s.mustCreateDeploymentLocked(&cid, nodeID, spec.Encode(), 0, "system deployment")
	slog.Info("created system deployment", "nodeID", nodeID, "version", opendeployVersion)
}

// EnsureNetproxyDeployment creates the per-node opendeploy-net internal
// deployment when missing. Existing desired state is administrator-managed.
func (s *Service) EnsureNetproxyDeployment(nodeID int32, initialVersion string) *apigen.DeploymentConfig {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	desiredVersion := strings.TrimSpace(initialVersion)
	if desiredVersion == "" {
		panic("EnsureNetproxyDeployment requires an explicit OpenDeploy version")
	}
	cid := apigen.DeploymentIdentity{
		SpaceID: OpendeploySpaceID,
		Name:    internaldeploy.NetproxyName,
	}
	desiredSpec := internaldeploy.NetproxySpec()

	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, cfg := range s.configCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, cid) && !cfg.Deleted {
			if err := desiredSpec.SetWorkloadState(cfg.WorkloadVersion(), cfg.WorkloadRunning()); err != nil {
				panic(fmt.Sprintf("compare netproxy deployment state: %v", err))
			}
			if !bytes.Equal(cfg.Spec.Encode(), desiredSpec.Encode()) {
				slog.Warn("repairing netproxy deployment spec", "nodeID", nodeID, "deploymentID", cfg.ID)
				s.repairDeploymentSpecLocked(cfg.ID, desiredSpec, "netproxy")
				cfg = s.configCache[cfg.ID]
			}
			return cfg
		}
	}

	spec := desiredSpec
	if err := spec.SetWorkloadState(desiredVersion, true); err != nil {
		panic(fmt.Sprintf("initialize netproxy deployment state: %v", err))
	}
	cfg := s.mustCreateDeploymentLocked(&cid, nodeID, spec.Encode(), 0, "netproxy deployment")
	slog.Info("created netproxy deployment", "nodeID", nodeID, "version", desiredVersion)
	return cfg
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)

	existing, err := s.q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig (%s repair): %v", label, err))
	}
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.Version)
	existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve %s deployment workload state: %v", label, err))
	}
	s.mustAppendConfigVersionLocked(existing, storedSpec.Encode(), 0, label+" deployment repair")
}
