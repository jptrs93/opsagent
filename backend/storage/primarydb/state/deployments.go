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
	dbID := int64(deploymentID)
	rows, err := s.q.ListDeploymentConfigHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentConfigHistory: %v", err))
	}
	// Get the identity and created_at from cache for display.
	var identity apigen.DeploymentIdentity
	var createdAt time.Time
	if cfg, ok := s.configCache[deploymentID]; ok {
		identity = cfg.Identity
		createdAt = cfg.CreatedAt
	}
	out := make([]*apigen.DeploymentConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, configHistoryRowToFullProto(r, identity, createdAt))
	}
	return out
}

// --- deployment config update ---

// DeploymentConfigUpdate is a full replacement of the mutable config fields,
// applied only when ExpectedVersion matches the next version of the stored row.
// Callers always hold the current row (they need it for ExpectedVersion), so
// unchanged fields are passed through as-is rather than merged from storage.
type DeploymentConfigUpdate struct {
	ExpectedVersion int32
	SpaceID         int32
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

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	specBlob := update.Spec.Encode()

	var cfg *apigen.DeploymentConfig
	var applied, compatible bool
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		existing, err := q.GetDeploymentConfig(bgCtx, dbID)
		if err != nil {
			panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
		}
		if update.ExpectedVersion != int32(existing.Version+1) {
			cfg, applied, compatible = getConfigRowToProto(existing), false, false
			return nil
		}

		spaceID := int64(update.SpaceID)
		deleted := boolToInt(update.Deleted)
		if spaceID == existing.SpaceID &&
			bytes.Equal(specBlob, existing.SpecBlob) &&
			deleted == existing.Deleted {
			cfg, applied, compatible = getConfigRowToProto(existing), false, true
			return nil
		}

		params := pq.UpsertDeploymentConfigParams{
			DeploymentID: dbID,
			NodeID:       existing.NodeID,
			SpaceID:      spaceID,
			Name:         existing.Name,
			CreatedAt:    existing.CreatedAt,
			Version:      existing.Version + 1,
			UpdatedAt:    now,
			UpdatedBy:    userID,
			SpecBlob:     specBlob,
			Deleted:      deleted,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      params.Version,
			UpdatedAt:    params.UpdatedAt,
			UpdatedBy:    params.UpdatedBy,
			SpaceID:      params.SpaceID,
			NodeID:       params.NodeID,
			SpecBlob:     params.SpecBlob,
			Deleted:      params.Deleted,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
		}
		cfg, applied, compatible = upsertParamsToProto(params), true, true
		return nil
	}); err != nil {
		panic(fmt.Sprintf("deployment config update tx: %v", err))
	}

	if applied {
		s.configCache[deploymentID] = cfg
		s.notifyConfigLocked(deploymentID)
	}
	return cfg, applied, compatible
}

func (s *Service) MustSetDeploymentWorkloadState(ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.mustSetDeploymentWorkloadStateLocked(ctx, deploymentID, version, running)
}

func (s *Service) mustSetDeploymentWorkloadStateLocked(ctx apigen.Context, deploymentID int32, version string, running bool) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	var params pq.UpsertDeploymentConfigParams
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		existing, err := q.GetDeploymentConfig(bgCtx, dbID)
		if err != nil {
			panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
		}

		spec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
		if err := spec.SetWorkloadState(version, running); err != nil {
			panic(fmt.Sprintf("update deployment workload state: %v", err))
		}
		params = pq.UpsertDeploymentConfigParams{
			DeploymentID: dbID,
			NodeID:       existing.NodeID,
			SpaceID:      existing.SpaceID,
			Name:         existing.Name,
			CreatedAt:    existing.CreatedAt,
			Version:      existing.Version + 1,
			UpdatedAt:    now,
			UpdatedBy:    userID,
			SpecBlob:     spec.Encode(),
			Deleted:      existing.Deleted,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      params.Version,
			UpdatedAt:    params.UpdatedAt,
			UpdatedBy:    params.UpdatedBy,
			SpaceID:      params.SpaceID,
			NodeID:       params.NodeID,
			SpecBlob:     params.SpecBlob,
			Deleted:      params.Deleted,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("deployment workload state tx: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
}

// --- deployment spec update ---

func (s *Service) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	if spec == nil {
		panic("deployment spec must not be nil")
	}

	var params pq.UpsertDeploymentConfigParams
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		existing, err := q.GetDeploymentConfig(bgCtx, dbID)
		if err != nil {
			panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
		}

		storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.Version)
		existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
		if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
			panic(fmt.Sprintf("preserve deployment workload state: %v", err))
		}
		specBlob := storedSpec.Encode()

		newVersion := existing.Version + 1
		params = pq.UpsertDeploymentConfigParams{
			DeploymentID: dbID,
			NodeID:       existing.NodeID,
			SpaceID:      existing.SpaceID,
			Name:         existing.Name,
			CreatedAt:    existing.CreatedAt,
			Version:      newVersion,
			UpdatedAt:    now,
			UpdatedBy:    userID,
			SpecBlob:     specBlob,
			Deleted:      existing.Deleted,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      newVersion,
			UpdatedAt:    now,
			UpdatedBy:    userID,
			SpaceID:      params.SpaceID,
			NodeID:       params.NodeID,
			SpecBlob:     specBlob,
			Deleted:      existing.Deleted,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("deployment spec update tx: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
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

	bgCtx := context.Background()
	now := time.Now().UnixMilli()

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	if spec == nil {
		panic("deployment spec must not be nil")
	}
	specBlob := spec.Encode()

	var dbID int64
	var row pq.CreateDeploymentConfigRow
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		var err error
		row, err = q.CreateDeploymentConfig(bgCtx, pq.CreateDeploymentConfigParams{
			NodeID:    int64(nodeID),
			SpaceID:   int64(cid.SpaceID),
			Name:      cid.Name,
			CreatedAt: now,
			Version:   1,
			UpdatedAt: now,
			UpdatedBy: userID,
			SpecBlob:  specBlob,
			Deleted:   0,
		})
		if err != nil {
			panic(fmt.Sprintf("CreateDeploymentConfig: %v", err))
		}
		dbID = row.DeploymentID

		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      1,
			UpdatedAt:    now,
			UpdatedBy:    userID,
			SpaceID:      int64(cid.SpaceID),
			NodeID:       int64(nodeID),
			SpecBlob:     specBlob,
			Deleted:      0,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory (create): %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("deployment create tx: %v", err))
	}

	cfg := upsertParamsToProto(pq.UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		NodeID:       int64(nodeID),
		SpaceID:      int64(cid.SpaceID),
		Name:         cid.Name,
		CreatedAt:    row.CreatedAt,
		Version:      1,
		UpdatedAt:    now,
		UpdatedBy:    userID,
		SpecBlob:     specBlob,
		Deleted:      0,
	})
	id := int32(dbID)
	s.configCache[id] = cfg
	s.notifyConfigLocked(id)
	return cfg
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
	specBlob := spec.Encode()

	bgCtx := context.Background()
	now := time.Now().UnixMilli()
	var dbID int64
	var row pq.CreateDeploymentConfigRow
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		var err error
		row, err = q.CreateDeploymentConfig(bgCtx, pq.CreateDeploymentConfigParams{
			NodeID:    int64(nodeID),
			SpaceID:   int64(cid.SpaceID),
			Name:      cid.Name,
			CreatedAt: now,
			Version:   1,
			UpdatedAt: now,
			SpecBlob:  specBlob,
			Deleted:   0,
		})
		if err != nil {
			panic(fmt.Sprintf("CreateDeploymentConfig (system): %v", err))
		}
		dbID = row.DeploymentID

		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      1,
			UpdatedAt:    now,
			SpaceID:      int64(cid.SpaceID),
			NodeID:       int64(nodeID),
			SpecBlob:     specBlob,
			Deleted:      0,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory (system): %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("system deployment create tx: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(pq.UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		NodeID:       int64(nodeID),
		SpaceID:      int64(cid.SpaceID),
		Name:         cid.Name,
		CreatedAt:    row.CreatedAt,
		Version:      1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	})
	s.notifyConfigLocked(id)
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
			s.Mu.Unlock()
			return cfg
		}
	}
	defer s.Mu.Unlock()

	spec := desiredSpec
	if err := spec.SetWorkloadState(desiredVersion, true); err != nil {
		panic(fmt.Sprintf("initialize netproxy deployment state: %v", err))
	}
	specBlob := spec.Encode()

	bgCtx := context.Background()
	now := time.Now().UnixMilli()
	var dbID int64
	var row pq.CreateDeploymentConfigRow
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		var err error
		row, err = q.CreateDeploymentConfig(bgCtx, pq.CreateDeploymentConfigParams{
			NodeID:    int64(nodeID),
			SpaceID:   int64(cid.SpaceID),
			Name:      cid.Name,
			CreatedAt: now,
			Version:   1,
			UpdatedAt: now,
			SpecBlob:  specBlob,
			Deleted:   0,
		})
		if err != nil {
			panic(fmt.Sprintf("CreateDeploymentConfig (netproxy): %v", err))
		}
		dbID = row.DeploymentID

		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      1,
			UpdatedAt:    now,
			SpaceID:      int64(cid.SpaceID),
			NodeID:       int64(nodeID),
			SpecBlob:     specBlob,
			Deleted:      0,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory (netproxy): %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("netproxy deployment create tx: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(pq.UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		NodeID:       int64(nodeID),
		SpaceID:      int64(cid.SpaceID),
		Name:         cid.Name,
		CreatedAt:    row.CreatedAt,
		Version:      1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	})
	s.notifyConfigLocked(id)
	slog.Info("created netproxy deployment", "nodeID", nodeID, "version", desiredVersion)
	return s.configCache[id]
}

func (s *Service) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	var params pq.UpsertDeploymentConfigParams
	if err := s.q.Tx(bgCtx, func(q *pq.Queries) error {
		existing, err := q.GetDeploymentConfig(bgCtx, dbID)
		if err != nil {
			panic(fmt.Sprintf("GetDeploymentConfig (%s repair): %v", label, err))
		}
		storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.Version)
		existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
		if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
			panic(fmt.Sprintf("preserve %s deployment workload state: %v", label, err))
		}
		specBlob := storedSpec.Encode()
		newVersion := existing.Version + 1
		params = pq.UpsertDeploymentConfigParams{
			DeploymentID: dbID,
			NodeID:       existing.NodeID,
			SpaceID:      existing.SpaceID,
			Name:         existing.Name,
			CreatedAt:    existing.CreatedAt,
			Version:      newVersion,
			UpdatedAt:    now,
			UpdatedBy:    0,
			SpecBlob:     specBlob,
			Deleted:      existing.Deleted,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig (%s repair): %v", label, err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, pq.InsertDeploymentConfigHistoryParams{
			DeploymentID: dbID,
			Version:      newVersion,
			UpdatedAt:    now,
			UpdatedBy:    0,
			SpaceID:      params.SpaceID,
			NodeID:       params.NodeID,
			SpecBlob:     params.SpecBlob,
			Deleted:      params.Deleted,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory (%s repair): %v", label, err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("%s deployment repair tx: %v", label, err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
}
