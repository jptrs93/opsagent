package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

const OpendeploySpaceID int32 = internaldeploy.SpaceID
const DefaultSpaceID int32 = 1

const systemDeploymentName = internaldeploy.SelfName
const systemDeploymentRepo = internaldeploy.Repo
const systemDeploymentBinPath = "/var/lib/opendeploy/bin/opendeploy"
const netproxyDeploymentName = internaldeploy.NetproxyName
const netproxyStateDir = "/var/lib/opendeploy/netproxy"
const netproxyFileDescriptorLimit = 65_536

func normalizedUserSpaceID(spaceID int32) int32 {
	if spaceID <= 0 {
		return DefaultSpaceID
	}
	return spaceID
}

func IsSystemDeploymentIdentity(identity apigen.DeploymentIdentity) bool {
	return internaldeploy.IsSelfIdentity(identity)
}

func IsSystemDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsSystemDeploymentIdentity(cfg.Identity)
}

func IsNetproxyDeploymentIdentity(identity apigen.DeploymentIdentity) bool {
	return internaldeploy.IsNetproxyIdentity(identity)
}

func IsNetproxyDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsNetproxyDeploymentIdentity(cfg.Identity)
}

func IsInternalDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && internaldeploy.IsInternalIdentity(cfg.Identity)
}

func SystemDeploymentSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		SystemdSpec: &apigen.SystemdSpec{
			Source: &apigen.GithubRelease{
				Repo:  systemDeploymentRepo,
				Asset: "opendeploy-linux-" + runtime.GOARCH,
			},
			Runtime: &apigen.SystemdRuntime{
				Name:    systemDeploymentName,
				BinPath: systemDeploymentBinPath,
			},
		},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
		},
	}
}

func isSystemDeploymentSpec(spec *apigen.DeploymentSpec) bool {
	if spec == nil || spec.SystemdSpec == nil || spec.SystemdSpec.Source == nil || spec.SystemdSpec.Runtime == nil {
		return false
	}
	gh := spec.SystemdSpec.Source
	sys := spec.SystemdSpec.Runtime
	return gh.Repo == systemDeploymentRepo &&
		gh.Asset == "opendeploy-linux-"+runtime.GOARCH &&
		sys.Name == systemDeploymentName &&
		sys.BinPath == systemDeploymentBinPath &&
		(spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST ||
			spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_UNSPECIFIED)
}

func NetproxyDeploymentSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source: apigen.ContainerBundleSource{
				RemoteImage: &apigen.RemoteDockerImage{Image: internaldeploy.NetproxyImage},
			},
			Runtime: apigen.ContainerRuntime{
				OverrideCommand:     []string{"/opendeploy", "dataplane"},
				DefaultVolume:       apigen.DefaultVolumeMount{Disabled: true},
				FileDescriptorLimit: netproxyFileDescriptorLimit,
				Mounts: []*apigen.CustomHostMount{{
					HostPath:      netproxyStateDir,
					ContainerPath: netproxyStateDir,
					Permission:    apigen.FilePermission_READ_ONLY,
				}},
			},
		},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		},
	}
}

type PrimaryStorage struct {
	*deploymentStore
	userSubs            *pubsubu.PubSub[apigen.User]
	backupStatusMu      sync.RWMutex
	backupStatus        apigen.BackupStatus
	backupStatusSubs    *pubsubu.PubSub[apigen.BackupStatus]
	secretStatusSubs    *pubsubu.PubSub[apigen.SecretsStatusResponse]
	secretSubs          *pubsubu.PubSub[apigen.SecretReference]
	secretMetaSubs      *pubsubu.PubSub[apigen.SecretMeta]
	userConfigSubs      *pubsubu.PubSub[apigen.UserConfigReference]
	userConfigValueSubs *pubsubu.PubSub[apigen.UserConfig]
	spaceSubs           *pubsubu.PubSub[apigen.Space]
	assetSubs           *pubsubu.PubSub[apigen.AssetMeta]
	enrollmentSubs      *pubsubu.PubSub[apigen.EnrollmentRequestStatus]
	nodeSubs            *pubsubu.PubSub[apigen.ClusterNode]
	nodeStatusSubs      *pubsubu.PubSub[apigen.ClusterNodeStatus]
}

func NewPrimaryStorage(dbPath string) *PrimaryStorage {
	db := mustInitPrimary(dbPath)
	return &PrimaryStorage{
		deploymentStore:     newDeploymentStore(db),
		userSubs:            &pubsubu.PubSub[apigen.User]{},
		backupStatusSubs:    &pubsubu.PubSub[apigen.BackupStatus]{},
		secretStatusSubs:    &pubsubu.PubSub[apigen.SecretsStatusResponse]{},
		secretSubs:          &pubsubu.PubSub[apigen.SecretReference]{},
		secretMetaSubs:      &pubsubu.PubSub[apigen.SecretMeta]{},
		userConfigSubs:      &pubsubu.PubSub[apigen.UserConfigReference]{},
		userConfigValueSubs: &pubsubu.PubSub[apigen.UserConfig]{},
		spaceSubs:           &pubsubu.PubSub[apigen.Space]{},
		assetSubs:           &pubsubu.PubSub[apigen.AssetMeta]{},
		enrollmentSubs:      &pubsubu.PubSub[apigen.EnrollmentRequestStatus]{},
		nodeSubs:            &pubsubu.PubSub[apigen.ClusterNode]{},
		nodeStatusSubs:      &pubsubu.PubSub[apigen.ClusterNodeStatus]{},
	}
}

func (s *PrimaryStorage) Close() error {
	return s.db.Close()
}

// ListActiveDeploymentConfigs returns all non-deleted configs from the cache.
func (s *PrimaryStorage) ListActiveDeploymentConfigs() []*apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *PrimaryStorage) InvalidateNodeRuntimeState(nodeID int32) (int64, error) {
	if nodeID <= 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		DELETE FROM scheduled_instance_status
		WHERE scheduled_instance_id IN (
			SELECT id FROM scheduled_instances
			WHERE node_id = ?
				AND deployment_id IN (
					SELECT deployment_id FROM deployment_configs
					WHERE NOT (space_id = ? AND name = ?)
				)
		)`, nodeID, OpendeploySpaceID, systemDeploymentName)
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count invalidated runtime state for node %d: %w", nodeID, err)
	}
	for _, state := range s.scheduledCache {
		if state.Instance.NodeID != nodeID {
			continue
		}
		if IsSystemDeploymentConfig(&state.Config) {
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

func (s *PrimaryStorage) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
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

func (s *PrimaryStorage) UpdateDeploymentConfig(ctx apigen.Context, deploymentID int32, update DeploymentConfigUpdate) (*apigen.DeploymentConfig, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *PrimaryStorage) MustSetDeploymentWorkloadState(ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustSetDeploymentWorkloadStateLocked(ctx, deploymentID, version, running)
}

func (s *PrimaryStorage) mustSetDeploymentWorkloadStateLocked(ctx apigen.Context, deploymentID int32, version string, running bool) {
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

func (s *PrimaryStorage) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *PrimaryStorage) MustUpdateDeploymentSpace(ctx apigen.Context, deploymentID int32, spaceID int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if existing.SpaceID == int64(spaceID) {
		return
	}
	newVersion := existing.Version + 1
	params := UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		NodeID:       existing.NodeID,
		SpaceID:      int64(spaceID),
		Name:         existing.Name,
		CreatedAt:    existing.CreatedAt,
		Version:      newVersion,
		UpdatedAt:    now,
		UpdatedBy:    userID,
		SpecBlob:     existing.SpecBlob,
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
		SpecBlob:     existing.SpecBlob,
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
func (s *PrimaryStorage) MustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	return s.mustCreateDeploymentForNode(ctx, cid, nodeID, spec)
}

func (s *PrimaryStorage) mustCreateDeploymentForNode(ctx apigen.Context, cid *apigen.DeploymentIdentity, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()

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
func (s *PrimaryStorage) EnsureSystemDeployment(nodeID int32, opendeployVersion string) {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	cid := apigen.DeploymentIdentity{
		SpaceID: OpendeploySpaceID,
		Name:    systemDeploymentName,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if it already exists.
	for _, cfg := range s.configCache {
		if storage.DeploymentKeyMatches(*cfg, nodeID, cid) && !cfg.Deleted {
			if !isSystemDeploymentSpec(&cfg.Spec) {
				slog.Warn("repairing system deployment spec", "nodeID", nodeID, "deploymentID", cfg.ID)
				s.repairDeploymentSpecLocked(cfg.ID, SystemDeploymentSpec(), "system")
			}
			return
		}
	}

	spec := SystemDeploymentSpec()
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
func (s *PrimaryStorage) EnsureNetproxyDeployment(nodeID int32, initialVersion string) *apigen.DeploymentConfig {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	desiredVersion := strings.TrimSpace(initialVersion)
	if desiredVersion == "" {
		panic("EnsureNetproxyDeployment requires an explicit OpenDeploy version")
	}
	cid := apigen.DeploymentIdentity{
		SpaceID: OpendeploySpaceID,
		Name:    netproxyDeploymentName,
	}
	desiredSpec := NetproxyDeploymentSpec()

	s.mu.Lock()
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
			s.mu.Unlock()
			return cfg
		}
	}
	defer s.mu.Unlock()

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

func (s *PrimaryStorage) repairDeploymentSpecLocked(deploymentID int32, spec *apigen.DeploymentSpec, label string) {
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

// timeToMillis / millisToTime convert a wall-clock time to/from the DB integer
// form (epoch ms), mapping the zero time to the 0 sentinel both ways so an
// unset created_at never surfaces as a 1970 timestamp.
func timeToMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// configProtoToUpsertParams builds upsert params from a full DeploymentConfig.
// Used by the secondary to persist configs pushed by the primary verbatim: the
// primary's integer ID is authoritative and written directly.
func configProtoToUpsertParams(cfg *apigen.DeploymentConfig) UpsertDeploymentConfigParams {
	var specBlob []byte
	if !cfg.Spec.IsZero() {
		specBlob = cfg.Spec.Encode()
	}
	return UpsertDeploymentConfigParams{
		DeploymentID: int64(cfg.ID),
		NodeID:       int64(cfg.NodeID),
		SpaceID:      int64(cfg.Identity.SpaceID),
		Name:         cfg.Identity.Name,
		CreatedAt:    timeToMillis(cfg.CreatedAt),
		Version:      int64(cfg.Version),
		UpdatedAt:    cfg.UpdatedAt.UnixMilli(),
		UpdatedBy:    int64(cfg.UpdatedBy),
		SpecBlob:     specBlob,
		Deleted:      boolToInt(cfg.Deleted),
	}
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

func (s *PrimaryStorage) WriteUser(user *apigen.InternalUser) {
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

func (s *PrimaryStorage) ListUsersPublic() []*apigen.User {
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

func (s *PrimaryStorage) SubscribeUserUpdates() (*pubsubu.Sub[apigen.User], func()) {
	sub := s.userSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) NotifyBackupStatusUpdate(status apigen.BackupStatus) {
	s.backupStatusMu.Lock()
	s.backupStatus = status
	s.backupStatusMu.Unlock()
	s.backupStatusSubs.Notify(status)
}

func (s *PrimaryStorage) CurrentBackupStatus() apigen.BackupStatus {
	s.backupStatusMu.RLock()
	defer s.backupStatusMu.RUnlock()
	return s.backupStatus
}

func (s *PrimaryStorage) SubscribeBackupStatusUpdates() (*pubsubu.Sub[apigen.BackupStatus], func()) {
	sub := s.backupStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) ListSecretReferences() []*apigen.SecretReference {
	rows, err := s.q.ListSecrets(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListSecrets: %v", err))
	}
	out := make([]*apigen.SecretReference, 0, len(rows))
	for _, row := range rows {
		out = append(out, &apigen.SecretReference{ID: int32(row.ID), Name: row.Name, SpaceID: int32(row.SpaceID), Version: int32(row.Version)})
	}
	return out
}

func (s *PrimaryStorage) NotifySecretReferenceUpdate(ref apigen.SecretReference) {
	s.secretSubs.Notify(ref)
}

func (s *PrimaryStorage) NotifySecretsStatusUpdate(status apigen.SecretsStatusResponse) {
	s.secretStatusSubs.Notify(status)
}

func (s *PrimaryStorage) SubscribeSecretsStatusUpdates() (*pubsubu.Sub[apigen.SecretsStatusResponse], func()) {
	sub := s.secretStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) NotifySecretMetaUpdate(meta apigen.SecretMeta) {
	s.secretMetaSubs.Notify(meta)
}

func (s *PrimaryStorage) SubscribeSecretReferenceUpdates() (*pubsubu.Sub[apigen.SecretReference], func()) {
	sub := s.secretSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) SubscribeSecretMetaUpdates() (*pubsubu.Sub[apigen.SecretMeta], func()) {
	sub := s.secretMetaSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) ListUserConfigReferences() []*apigen.UserConfigReference {
	rows, err := s.q.ListAllUserConfigs(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAllUserConfigs: %v", err))
	}
	out := make([]*apigen.UserConfigReference, 0, len(rows))
	for _, row := range rows {
		out = append(out, &apigen.UserConfigReference{ID: int32(row.ID), Name: row.Name, SpaceID: int32(row.SpaceID), Version: int32(row.Version)})
	}
	return out
}

func (s *PrimaryStorage) SubscribeUserConfigReferenceUpdates() (*pubsubu.Sub[apigen.UserConfigReference], func()) {
	sub := s.userConfigSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) SubscribeUserConfigValueUpdates() (*pubsubu.Sub[apigen.UserConfig], func()) {
	sub := s.userConfigValueSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) ListSpaces() []*apigen.Space {
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

func (s *PrimaryStorage) CreateSpace(name string) (*apigen.Space, error) {
	row, err := s.q.CreateSpace(context.Background(), name)
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *PrimaryStorage) UpdateSpace(id int32, name string) (*apigen.Space, error) {
	row, err := s.q.UpdateSpace(context.Background(), UpdateSpaceParams{Name: name, ID: int64(id)})
	if err != nil {
		return nil, err
	}
	space := spaceRowToProto(row)
	s.spaceSubs.Notify(*space)
	return space, nil
}

func (s *PrimaryStorage) DeleteSpace(id int32) error {
	if err := s.q.DeleteSpace(context.Background(), int64(id)); err != nil {
		return err
	}
	s.spaceSubs.Notify(apigen.Space{ID: id, Deleted: true})
	return nil
}

func (s *PrimaryStorage) CountDeploymentsForSpace(id int32) (int64, error) {
	return s.q.CountDeploymentsForSpace(context.Background(), int64(id))
}

func (s *PrimaryStorage) SubscribeSpaceUpdates() (*pubsubu.Sub[apigen.Space], func()) {
	sub := s.spaceSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) FetchUserByID(id int32) (*apigen.InternalUser, error) {
	row, err := s.q.GetUser(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(row.DataBlob)
}

func (s *PrimaryStorage) FetchUserMatching(predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
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

func (s *PrimaryStorage) UpdateUserMatching(predicate func(*apigen.InternalUser) bool, f func(*apigen.InternalUser)) {
	user, err := s.FetchUserMatching(predicate)
	if err != nil {
		panic(fmt.Sprintf("UpdateUserMatching: %v", err))
	}
	f(user)
	s.WriteUser(user)
}

// --- auth: agent sessions ---

// AgentSession is a stored agent session. TokenHash is the SHA-256 of the
// issued token; the plaintext is never persisted.
type AgentSessionRecord struct {
	ID          string
	UserID      int32
	CreatedAt   time.Time
	ExpiresAt   time.Time
	TokenHash   []byte
	TokenPrefix string
	RevokedAt   time.Time
	Scopes      []string
}

func agentSessionRowToRecord(row AgentSession) AgentSessionRecord {
	rec := AgentSessionRecord{
		ID:          row.ID,
		UserID:      int32(row.UserID),
		CreatedAt:   time.Unix(row.CreatedAt, 0),
		ExpiresAt:   time.Unix(row.ExpiresAt, 0),
		TokenHash:   row.TokenHash,
		TokenPrefix: row.TokenPrefix,
	}
	if row.RevokedAt > 0 {
		rec.RevokedAt = time.Unix(row.RevokedAt, 0)
	}
	if row.Scopes != "" {
		rec.Scopes = strings.Split(row.Scopes, ",")
	}
	return rec
}

func (s *PrimaryStorage) InsertAgentSession(rec AgentSessionRecord) error {
	return s.q.InsertAgentSession(context.Background(), InsertAgentSessionParams{
		ID:          rec.ID,
		UserID:      int64(rec.UserID),
		CreatedAt:   rec.CreatedAt.Unix(),
		ExpiresAt:   rec.ExpiresAt.Unix(),
		TokenHash:   rec.TokenHash,
		TokenPrefix: rec.TokenPrefix,
		Scopes:      strings.Join(rec.Scopes, ","),
	})
}

// FetchAgentSession returns ErrNotFound when no session carries the id, which
// is the normal case for a token minted before this table existed.
func (s *PrimaryStorage) FetchAgentSession(id string) (AgentSessionRecord, error) {
	row, err := s.q.GetAgentSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSessionRecord{}, ErrNotFound
	}
	if err != nil {
		return AgentSessionRecord{}, err
	}
	return agentSessionRowToRecord(row), nil
}

func (s *PrimaryStorage) ListAgentSessionsForUser(userID int32) ([]AgentSessionRecord, error) {
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

// RevokeAgentSession is scoped by user id so one operator cannot revoke
// another's session by guessing its id.
func (s *PrimaryStorage) RevokeAgentSession(id string, userID int32, at time.Time) error {
	return s.q.RevokeAgentSession(context.Background(), RevokeAgentSessionParams{
		RevokedAt: at.Unix(),
		ID:        id,
		UserID:    int64(userID),
	})
}

func (s *PrimaryStorage) UserCount() int {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("UserCount: %v", err))
	}
	return len(rows)
}

func (s *PrimaryStorage) FetchLatestOpenDeployConfig() (SystemConfigRevision, error) {
	r, err := s.q.GetLatestConfig(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return SystemConfigRevision{}, ErrNotFound
	}
	return r, err
}

func (s *PrimaryStorage) AppendOpenDeploySettings(blob []byte) (int64, error) {
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
func (s *PrimaryStorage) WritePublicKey(rec *apigen.PublicKeyRecord) {
	ctx := context.Background()
	if err := s.q.UpsertPublicKey(ctx, UpsertPublicKeyParams{
		Kid:      rec.Kid,
		KeyBytes: rec.KeyBytes,
	}); err != nil {
		panic(fmt.Sprintf("UpsertPublicKey: %v", err))
	}
}

func (s *PrimaryStorage) FetchPublicKey(kid string) (*apigen.PublicKeyRecord, error) {
	row, err := s.q.GetPublicKey(context.Background(), kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &apigen.PublicKeyRecord{Kid: row.Kid, KeyBytes: row.KeyBytes}, nil
}
