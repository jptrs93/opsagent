package primarydb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/instancecache"
)

const OpendeploySpaceID int32 = internaldeploy.SpaceID
const DefaultSpaceID int32 = 1

func normalizedUserSpaceID(spaceID int32) int32 {
	if spaceID <= 0 {
		return DefaultSpaceID
	}
	return spaceID
}

type Storage struct {
	// Cache is the shared scheduled-instance runtime view. Its Mu is the
	// storage-wide mutex: every method that used to lock the deployment
	// store's mutex locks it.
	*instancecache.Cache

	db *sql.DB
	q  *Queries

	// configCache holds the latest desired DeploymentConfig per deployment id.
	// Used by primary scheduler/APIs only — never as the pinned config source for
	// a scheduled-instance snapshot.
	configCache map[int32]*apigen.DeploymentConfig
	// latestFinalCache retains the last incarnation of an ordinal after it is
	// finalized, so a stopped deployment can still show how its final run ended.
	// At most one entry per ordinal, and only while no live instance supersedes
	// it, so it never competes with the live cache for the same ordinal.
	latestFinalCache map[instanceOrdinalKey]*apigen.ScheduledInstanceState

	configSubs       *pubsubu.PubSub[apigen.DeploymentConfig]
	userSubs         *pubsubu.PubSub[apigen.User]
	backupStatusMu   sync.RWMutex
	backupStatus     apigen.BackupStatus
	backupStatusSubs *pubsubu.PubSub[apigen.BackupStatus]
	secretStatusSubs *pubsubu.PubSub[apigen.SecretsStatusResponse]
	secretMetaSubs   *pubsubu.PubSub[apigen.SecretMeta]
	userConfigSubs   *pubsubu.PubSub[apigen.ConfigMeta]
	spaceSubs        *pubsubu.PubSub[apigen.Space]
	assetSubs        *pubsubu.PubSub[apigen.AssetMeta]
	enrollmentSubs   *pubsubu.PubSub[apigen.EnrollmentRequestStatus]
	nodeSubs         *pubsubu.PubSub[apigen.ClusterNode]
	nodeStatusSubs   *pubsubu.PubSub[apigen.ClusterNodeStatus]
	// Carries the storage record rather than the proto, because subscribers have
	// to filter by user id before yielding: an agent session belongs to one
	// operator, unlike everything else broadcast here.
	agentSessionSubs *pubsubu.PubSub[AgentSessionRecord]
}

func Open(dbPath string) *Storage {
	db := mustInit(dbPath)
	s := &Storage{
		db:               db,
		q:                New(db),
		configCache:      make(map[int32]*apigen.DeploymentConfig),
		latestFinalCache: make(map[instanceOrdinalKey]*apigen.ScheduledInstanceState),
		configSubs:       &pubsubu.PubSub[apigen.DeploymentConfig]{},
		userSubs:         &pubsubu.PubSub[apigen.User]{},
		backupStatusSubs: &pubsubu.PubSub[apigen.BackupStatus]{},
		secretStatusSubs: &pubsubu.PubSub[apigen.SecretsStatusResponse]{},
		secretMetaSubs:   &pubsubu.PubSub[apigen.SecretMeta]{},
		userConfigSubs:   &pubsubu.PubSub[apigen.ConfigMeta]{},
		spaceSubs:        &pubsubu.PubSub[apigen.Space]{},
		assetSubs:        &pubsubu.PubSub[apigen.AssetMeta]{},
		enrollmentSubs:   &pubsubu.PubSub[apigen.EnrollmentRequestStatus]{},
		nodeSubs:         &pubsubu.PubSub[apigen.ClusterNode]{},
		nodeStatusSubs:   &pubsubu.PubSub[apigen.ClusterNodeStatus]{},
		agentSessionSubs: &pubsubu.PubSub[AgentSessionRecord]{},
	}
	s.Cache = instancecache.New(s.persistStatus)
	s.loadCache()
	return s
}

// persistStatus is the instancecache persistence hook: it durably appends a
// status row, panicking on failure per the storage error policy.
func (s *Storage) persistStatus(ctx context.Context, st *apigen.ScheduledInstanceStatus) {
	if err := s.q.InsertScheduledInstanceStatus(ctx, scheduledInstanceStatusProtoToInsertParams(st)); err != nil {
		panic(fmt.Sprintf("InsertScheduledInstanceStatus: %v", err))
	}
}

func (s *Storage) Close() error {
	return s.db.Close()
}

// ListActiveDeploymentConfigs returns all non-deleted configs from the cache.
func (s *Storage) ListActiveDeploymentConfigs() []*apigen.DeploymentConfig {
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

// InvalidateNodeRuntimeState clears node-local observations that cannot
// survive restoring the primary database onto a replacement host. Desired
// config remains unchanged; scheduled instance statuses for the node are cleared.
func (s *Storage) InvalidateNodeRuntimeState(nodeID int32) (int64, error) {
	if nodeID <= 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()

	result, err := s.db.Exec(`
		DELETE FROM scheduled_instance_status
		WHERE scheduled_instance_id IN (
			SELECT id FROM scheduled_instances
			WHERE node_id = ?
				AND deployment_id IN (
					SELECT deployment_id FROM deployment_configs
					WHERE NOT (space_id = ? AND name = ?)
				)
		)`, nodeID, OpendeploySpaceID, internaldeploy.SelfName)
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count invalidated runtime state for node %d: %w", nodeID, err)
	}
	for _, state := range s.Scheduled {
		if state.Instance.NodeID != nodeID {
			continue
		}
		if internaldeploy.IsSelfConfig(&state.Config) {
			continue
		}
		state.Status = apigen.ScheduledInstanceStatus{}
	}
	return count, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// --- deployment history ---

func (s *Storage) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
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

type DeploymentConfigUpdate struct {
	ExpectedVersion int32
	SpaceID         *int32
	Spec            *apigen.DeploymentSpec
	Deleted         *bool
}

func (s *Storage) UpdateDeploymentConfig(ctx apigen.Context, deploymentID int32, update DeploymentConfigUpdate) (*apigen.DeploymentConfig, bool, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	existing, err := q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}
	if update.ExpectedVersion != int32(existing.Version+1) {
		if err := tx.Commit(); err != nil {
			panic(fmt.Sprintf("commit: %v", err))
		}
		return getConfigRowToProto(existing), false, false
	}

	spec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	specBlob := existing.SpecBlob
	if update.Spec != nil {
		spec = mustDecodeDeploymentSpec(update.Spec.Encode(), dbID, existing.Version)
	}
	spaceID := existing.SpaceID
	if update.SpaceID != nil {
		spaceID = int64(*update.SpaceID)
	}
	if update.Spec != nil {
		specBlob = spec.Encode()
	}
	deleted := existing.Deleted
	if update.Deleted != nil {
		deleted = boolToInt(*update.Deleted)
	}

	if spaceID == existing.SpaceID &&
		bytes.Equal(specBlob, existing.SpecBlob) &&
		deleted == existing.Deleted {
		if err := tx.Commit(); err != nil {
			panic(fmt.Sprintf("commit: %v", err))
		}
		return getConfigRowToProto(existing), false, true
	}

	params := UpsertDeploymentConfigParams{
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
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(params)
	s.configCache[deploymentID] = cfg
	s.notifyConfigLocked(deploymentID)
	return cfg, true, true
}

func (s *Storage) MustSetDeploymentWorkloadState(ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.mustSetDeploymentWorkloadStateLocked(ctx, deploymentID, version, running)
}

func (s *Storage) mustSetDeploymentWorkloadStateLocked(ctx apigen.Context, deploymentID int32, version string, running bool) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}
	existing, err := q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}

	spec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(fmt.Sprintf("update deployment workload state: %v", err))
	}
	params := UpsertDeploymentConfigParams{
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
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
}

// --- deployment spec update ---

func (s *Storage) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	existing, err := q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig: %v", err))
	}

	if spec == nil {
		panic("deployment spec must not be nil")
	}
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), dbID, existing.Version)
	existingSpec := mustDecodeDeploymentSpec(existing.SpecBlob, dbID, existing.Version)
	if err := storedSpec.SetWorkloadState(existingSpec.WorkloadVersion(), existingSpec.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment workload state: %v", err))
	}
	specBlob := storedSpec.Encode()

	newVersion := existing.Version + 1
	params := UpsertDeploymentConfigParams{
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
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
}

// MustCreateDeploymentForNode creates a deployment with an explicit canonical
// node assignment.
func (s *Storage) MustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	return s.mustCreateDeploymentForNode(ctx, cid, nodeID, spec)
}

func (s *Storage) mustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
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

	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)

	if spec == nil {
		panic("deployment spec must not be nil")
	}
	specBlob := spec.Encode()

	row, err := q.CreateDeploymentConfig(bgCtx, CreateDeploymentConfigParams{
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
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(UpsertDeploymentConfigParams{
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
func (s *Storage) EnsureSystemDeployment(nodeID int32, opendeployVersion string) {
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
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	row, err := q.CreateDeploymentConfig(bgCtx, CreateDeploymentConfigParams{
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
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(UpsertDeploymentConfigParams{
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
func (s *Storage) EnsureNetproxyDeployment(nodeID int32, initialVersion string) *apigen.DeploymentConfig {
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
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	row, err := q.CreateDeploymentConfig(bgCtx, CreateDeploymentConfigParams{
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
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(UpsertDeploymentConfigParams{
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

func (s *Storage) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

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
	params := UpsertDeploymentConfigParams{
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
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID: dbID,
		Version:      newVersion,
		UpdatedAt:    now,
		UpdatedBy:    0,
		SpaceID:      params.SpaceID,
		NodeID:       params.NodeID,
		SpecBlob:     specBlob,
		Deleted:      existing.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (%s repair): %v", label, err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyConfigLocked(deploymentID)
}

// --- row <-> proto conversions ---

// clockToNanos serializes a status HLC clock to its DB integer form (unix
// nanoseconds). Zero time maps to 0 — the "no status yet" placeholder sentinel.
func clockToNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// nanosToClock is the inverse of clockToNanos.
func nanosToClock(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// millisToTime converts the DB integer form (epoch ms) to a wall-clock time,
// mapping the 0 sentinel to the zero time so an unset created_at never
// surfaces as a 1970 timestamp.
func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func configRowToProto(r ListAllDeploymentConfigsRow) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(r.SpecBlob, r.DeploymentID, r.Version)
	return &apigen.DeploymentConfig{
		ID:     int32(r.DeploymentID),
		NodeID: int32(r.NodeID),
		Identity: apigen.DeploymentIdentity{
			SpaceID: int32(r.SpaceID),
			Name:    r.Name,
		},
		CreatedAt: millisToTime(r.CreatedAt),
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   r.Deleted != 0,
	}
}

func getConfigRowToProto(r GetDeploymentConfigRow) *apigen.DeploymentConfig {
	return configRowToProto(ListAllDeploymentConfigsRow{
		DeploymentID: r.DeploymentID,
		NodeID:       r.NodeID,
		SpaceID:      r.SpaceID,
		Name:         r.Name,
		CreatedAt:    r.CreatedAt,
		Version:      r.Version,
		UpdatedAt:    r.UpdatedAt,
		UpdatedBy:    r.UpdatedBy,
		SpecBlob:     r.SpecBlob,
		Deleted:      r.Deleted,
	})
}

func upsertParamsToProto(p UpsertDeploymentConfigParams) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(p.SpecBlob, p.DeploymentID, p.Version)
	return &apigen.DeploymentConfig{
		ID:     int32(p.DeploymentID),
		NodeID: int32(p.NodeID),
		Identity: apigen.DeploymentIdentity{
			SpaceID: int32(p.SpaceID),
			Name:    p.Name,
		},
		CreatedAt: millisToTime(p.CreatedAt),
		Version:   int32(p.Version),
		UpdatedAt: time.UnixMilli(p.UpdatedAt),
		UpdatedBy: int32(p.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   p.Deleted != 0,
	}
}

func deploymentSpecValue(spec *apigen.DeploymentSpec) apigen.DeploymentSpec {
	if spec == nil {
		return apigen.DeploymentSpec{}
	}
	return *spec
}

func mustDecodeDeploymentSpec(blob []byte, deploymentID, version int64) *apigen.DeploymentSpec {
	spec, err := apigen.DecodeDeploymentSpec(blob)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d version %d spec: %v", deploymentID, version, err))
	}
	return spec
}

func spaceRowToProto(row Space) *apigen.Space {
	return &apigen.Space{ID: int32(row.ID), Name: row.Name}
}

// --- auth: users ---

var ErrNotFound = fmt.Errorf("not found")

func (s *Storage) WriteUser(user *apigen.InternalUser) {
	ctx := context.Background()
	if err := s.q.UpsertUser(ctx, UpsertUserParams{
		ID:       int64(user.ID),
		Name:     user.Name,
		DataBlob: user.Encode(),
	}); err != nil {
		panic(fmt.Sprintf("UpsertUser: %v", err))
	}
	s.userSubs.Notify(apigen.User{ID: user.ID, Name: user.Name})
}

func (s *Storage) ListUsersPublic() []*apigen.User {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListUsersPublic: %v", err))
	}
	out := make([]*apigen.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, &apigen.User{ID: int32(row.ID), Name: row.Name})
	}
	return out
}

func (s *Storage) SubscribeUserUpdates() (*pubsubu.Sub[apigen.User], func()) {
	sub := s.userSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) NotifyBackupStatusUpdate(status apigen.BackupStatus) {
	s.backupStatusMu.Lock()
	s.backupStatus = status
	s.backupStatusMu.Unlock()
	s.backupStatusSubs.Notify(status)
}

func (s *Storage) CurrentBackupStatus() apigen.BackupStatus {
	s.backupStatusMu.RLock()
	defer s.backupStatusMu.RUnlock()
	return s.backupStatus
}

func (s *Storage) SubscribeBackupStatusUpdates() (*pubsubu.Sub[apigen.BackupStatus], func()) {
	sub := s.backupStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) NotifySecretsStatusUpdate(status apigen.SecretsStatusResponse) {
	s.secretStatusSubs.Notify(status)
}

func (s *Storage) SubscribeSecretsStatusUpdates() (*pubsubu.Sub[apigen.SecretsStatusResponse], func()) {
	sub := s.secretStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) NotifySecretMetaUpdate(meta apigen.SecretMeta) {
	s.secretMetaSubs.Notify(meta)
}

func (s *Storage) SubscribeSecretMetaUpdates() (*pubsubu.Sub[apigen.SecretMeta], func()) {
	sub := s.secretMetaSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) NotifyConfigMetaUpdate(meta apigen.ConfigMeta) {
	s.userConfigSubs.Notify(meta)
}

func (s *Storage) SubscribeConfigMetaUpdates() (*pubsubu.Sub[apigen.ConfigMeta], func()) {
	sub := s.userConfigSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) ListSpaces() []*apigen.Space {
	rows, err := s.q.ListSpaces(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSpaces: %v", err))
	}
	out := make([]*apigen.Space, 0, len(rows))
	for _, row := range rows {
		out = append(out, spaceRowToProto(row))
	}
	return out
}

func (s *Storage) CreateSpace(name string) (*apigen.Space, error) {
	row, err := s.q.CreateSpace(context.Background(), name)
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	// Open the new space on every node before announcing it, so nothing can
	// observe a space that exists but is placeable nowhere. A node allows every
	// space that existed when it was enrolled; this keeps that true for spaces
	// created afterwards, and leaves the allow list purely an opt-out tool.
	s.AllowSpaceOnAllNodes(space.ID)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *Storage) UpdateSpace(id int32, name string) (*apigen.Space, error) {
	row, err := s.q.UpdateSpace(context.Background(), UpdateSpaceParams{Name: name, ID: int64(id)})
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *Storage) DeleteSpace(id int32) error {
	if err := s.q.DeleteSpace(context.Background(), int64(id)); err != nil {
		return err
	}
	// Otherwise ids of spaces that no longer exist accumulate in every node's
	// allow list, and a later space reusing the id would inherit them.
	s.RemoveSpaceFromAllNodes(id)
	s.spaceSubs.Notify(apigen.Space{ID: id, Deleted: true})
	return nil
}

func (s *Storage) CountDeploymentsForSpace(id int32) (int64, error) {
	return s.q.CountDeploymentsForSpace(context.Background(), int64(id))
}

func (s *Storage) SubscribeSpaceUpdates() (*pubsubu.Sub[apigen.Space], func()) {
	sub := s.spaceSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *Storage) FetchUserByID(id int32) (*apigen.InternalUser, error) {
	row, err := s.q.GetUser(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(row.DataBlob)
}

func (s *Storage) FetchUserMatching(predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		u, err := apigen.DecodeInternalUser(row.DataBlob)
		if err != nil {
			continue
		}
		if predicate(u) {
			return u, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Storage) UpdateUserMatching(predicate func(*apigen.InternalUser) bool, f func(*apigen.InternalUser)) {
	user, err := s.FetchUserMatching(predicate)
	if err != nil {
		panic(fmt.Sprintf("UpdateUserMatching: %v", err))
	}
	f(user)
	s.WriteUser(user)
}

// --- auth: agent sessions ---

// AgentSession is a stored agent session. TokenHash is the SHA-256 of the
// issued token; the plaintext is never persisted. A session that is still a
// pending request has no token at all: TokenHash, TokenPrefix, and ExpiresAt
// stay zero until it is approved and collected.
type AgentSessionRecord struct {
	ID                string
	UserID            int32
	CreatedAt         time.Time
	ExpiresAt         time.Time
	TokenHash         []byte
	TokenPrefix       string
	RevokedAt         time.Time
	Scopes            []string
	Status            apigen.AgentSessionStatus
	RequestingAddress string
	ApprovalCode      string
	ApprovedAt        time.Time
}

// Collected reports whether this session's token has been minted. Approval on
// its own does not mint one, so this is what separates a session waiting to be
// picked up from a live one.
func (r AgentSessionRecord) Collected() bool { return len(r.TokenHash) > 0 }

// unixOrZero keeps a zero time.Time as a zero column rather than the 1970
// epoch, so "never approved" and "approved at the epoch" stay distinguishable.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func agentSessionRowToRecord(row AgentSession) AgentSessionRecord {
	rec := AgentSessionRecord{
		ID:                row.ID,
		UserID:            int32(row.UserID),
		CreatedAt:         time.Unix(row.CreatedAt, 0),
		ExpiresAt:         timeOrZero(row.ExpiresAt),
		TokenHash:         row.TokenHash,
		TokenPrefix:       row.TokenPrefix,
		RevokedAt:         timeOrZero(row.RevokedAt),
		Status:            apigen.AgentSessionStatus(row.Status),
		RequestingAddress: row.RequestingAddress,
		ApprovalCode:      row.ApprovalCode,
		ApprovedAt:        timeOrZero(row.ApprovedAt),
	}
	if row.Scopes != "" {
		rec.Scopes = strings.Split(row.Scopes, ",")
	}
	return rec
}

func (s *Storage) InsertAgentSession(rec AgentSessionRecord) error {
	// A pending request has no token yet, and a nil slice would land as NULL
	// against a NOT NULL column.
	tokenHash := rec.TokenHash
	if tokenHash == nil {
		tokenHash = []byte{}
	}
	err := s.q.InsertAgentSession(context.Background(), InsertAgentSessionParams{
		ID:                rec.ID,
		UserID:            int64(rec.UserID),
		CreatedAt:         rec.CreatedAt.Unix(),
		ExpiresAt:         unixOrZero(rec.ExpiresAt),
		TokenHash:         tokenHash,
		TokenPrefix:       rec.TokenPrefix,
		Scopes:            strings.Join(rec.Scopes, ","),
		Status:            int64(rec.Status),
		RequestingAddress: rec.RequestingAddress,
		ApprovalCode:      rec.ApprovalCode,
		ApprovedAt:        unixOrZero(rec.ApprovedAt),
	})
	if err != nil {
		return err
	}
	s.agentSessionSubs.Notify(rec)
	return nil
}

// FetchAgentSession returns ErrNotFound when no session carries the id, which
// is the normal case for a token minted before this table existed.
func (s *Storage) FetchAgentSession(id string) (AgentSessionRecord, error) {
	row, err := s.q.GetAgentSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSessionRecord{}, ErrNotFound
	}
	if err != nil {
		return AgentSessionRecord{}, err
	}
	return agentSessionRowToRecord(row), nil
}

func (s *Storage) ListAgentSessionsForUser(userID int32) ([]AgentSessionRecord, error) {
	rows, err := s.q.ListAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]AgentSessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// ListPendingAgentSessionsForUser returns the user's open session requests,
// newest first. There is normally at most one, but stale requests are only
// closed lazily, so a caller has to be prepared for several.
func (s *Storage) ListPendingAgentSessionsForUser(userID int32) ([]AgentSessionRecord, error) {
	rows, err := s.q.ListPendingAgentSessionsForUser(context.Background(), int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]AgentSessionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSessionRowToRecord(row))
	}
	return out, nil
}

// SetAgentSessionStatus moves a session to a new state. approvedAt and revokedAt
// are written together with it so the row can never claim a state whose
// timestamp is missing.
func (s *Storage) SetAgentSessionStatus(id string, status apigen.AgentSessionStatus, approvedAt, revokedAt time.Time) error {
	err := s.q.SetAgentSessionStatus(context.Background(), SetAgentSessionStatusParams{
		Status:     int64(status),
		ApprovedAt: unixOrZero(approvedAt),
		RevokedAt:  unixOrZero(revokedAt),
		ID:         id,
	})
	if err != nil {
		return err
	}
	s.notifyAgentSession(id)
	return nil
}

// ApproveAgentSession approves a pending request and records the approver's
// scopes in the same statement. It reports false when the row was not pending,
// which is how a second approval of the same request is rejected.
func (s *Storage) ApproveAgentSession(id string, userID int32, scopes []string, at time.Time) (bool, error) {
	rows, err := s.q.ApproveAgentSession(context.Background(), ApproveAgentSessionParams{
		ApprovedAt: at.Unix(),
		Scopes:     strings.Join(scopes, ","),
		ID:         id,
		UserID:     int64(userID),
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	s.notifyAgentSession(id)
	return true, nil
}

// ClaimAgentSessionToken records a freshly minted token against an approved
// session and reports whether this caller was the one that claimed it. A false
// return means another request got there first; the caller must discard the
// token it minted rather than hand out a second working credential.
func (s *Storage) ClaimAgentSessionToken(id string, tokenHash []byte, tokenPrefix string, expiresAt time.Time, scopes []string) (bool, error) {
	rows, err := s.q.ClaimAgentSessionToken(context.Background(), ClaimAgentSessionTokenParams{
		TokenHash:   tokenHash,
		TokenPrefix: tokenPrefix,
		ExpiresAt:   unixOrZero(expiresAt),
		Scopes:      strings.Join(scopes, ","),
		ID:          id,
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	s.notifyAgentSession(id)
	return true, nil
}

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id.
func (s *Storage) RevokeAgentSession(id string, userID int32, status apigen.AgentSessionStatus, at time.Time) error {
	err := s.q.RevokeAgentSession(context.Background(), RevokeAgentSessionParams{
		RevokedAt: at.Unix(),
		Status:    int64(status),
		ID:        id,
		UserID:    int64(userID),
	})
	if err != nil {
		return err
	}
	s.notifyAgentSession(id)
	return nil
}

// SubscribeAgentSessionUpdates streams every agent session change. Records are
// not filtered here: subscribers must drop the ones whose UserID is not theirs.
func (s *Storage) SubscribeAgentSessionUpdates() (*pubsubu.Sub[AgentSessionRecord], func()) {
	sub := s.agentSessionSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

// notifyAgentSession re-reads the row so subscribers always see the persisted
// state rather than a caller's idea of it. A failed read is dropped: a missed
// update degrades the live list, which the next reconnect repairs, and is not
// worth failing the write that just succeeded.
func (s *Storage) notifyAgentSession(id string) {
	rec, err := s.FetchAgentSession(id)
	if err != nil {
		return
	}
	s.agentSessionSubs.Notify(rec)
}

func (s *Storage) UserCount() int {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("UserCount: %v", err))
	}
	return len(rows)
}

func (s *Storage) FetchLatestOpenDeployConfig() (SystemConfigRevision, error) {
	r, err := s.q.GetLatestConfig(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return SystemConfigRevision{}, ErrNotFound
	}
	return r, err
}

func (s *Storage) AppendOpenDeploySettings(blob []byte) (int64, error) {
	res, err := s.db.ExecContext(context.Background(), `
INSERT INTO system_config_revisions (updated_at, config_blob) VALUES (?, ?)
`, time.Now().UnixMilli(), blob)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// --- auth: public keys ---
func (s *Storage) WritePublicKey(rec *apigen.PublicKeyRecord) {
	ctx := context.Background()
	if err := s.q.UpsertPublicKey(ctx, UpsertPublicKeyParams{
		Kid:      rec.Kid,
		KeyBytes: rec.KeyBytes,
	}); err != nil {
		panic(fmt.Sprintf("UpsertPublicKey: %v", err))
	}
}

func (s *Storage) FetchPublicKey(kid string) (*apigen.PublicKeyRecord, error) {
	row, err := s.q.GetPublicKey(context.Background(), kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &apigen.PublicKeyRecord{Kid: row.Kid, KeyBytes: row.KeyBytes}, nil
}
