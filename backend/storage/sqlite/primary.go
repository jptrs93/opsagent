package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const OpendeploySpaceID int32 = internaldeploy.SpaceID
const DefaultSpaceID int32 = 1

const systemDeploymentName = internaldeploy.SelfName
const systemDeploymentRepo = internaldeploy.Repo
const systemDeploymentBinPath = "/var/lib/opendeploy/bin/opendeploy"
const dataplaneDeploymentName = internaldeploy.DataplaneName
const dataplaneStateDir = "/var/lib/opendeploy/dataplane"

func normalizedUserSpaceID(spaceID int32) int32 {
	if spaceID <= 0 {
		return DefaultSpaceID
	}
	return spaceID
}

func IsSystemDeploymentIdentifier(cid apigen.DeploymentIdentifier) bool {
	return internaldeploy.IsSelfIdentifier(cid)
}

func IsSystemDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsSystemDeploymentIdentifier(cfg.ConfigID)
}

func IsDataplaneDeploymentIdentifier(cid apigen.DeploymentIdentifier) bool {
	return internaldeploy.IsDataplaneIdentifier(cid)
}

func IsDataplaneDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return internaldeploy.IsDataplaneConfig(cfg)
}

func IsInternalDeploymentConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && internaldeploy.IsInternalIdentifier(cfg.ConfigID)
}

func SystemDeploymentSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			GithubRelease: &apigen.GithubReleaseConfig{
				Repo:  systemDeploymentRepo,
				Asset: "opendeploy-linux-" + runtime.GOARCH,
			},
		},
		Runner: apigen.RunnerConfig{
			Systemd: apigen.SystemdRunnerConfig{
				Name:    systemDeploymentName,
				BinPath: systemDeploymentBinPath,
			},
		},
	}
}

func isSystemDeploymentSpec(spec *apigen.DeploymentSpec) bool {
	if spec == nil {
		return false
	}
	gh := spec.Prepare.GithubRelease
	sys := spec.Runner.Systemd
	return gh != nil &&
		gh.Repo == systemDeploymentRepo &&
		gh.Asset == "opendeploy-linux-"+runtime.GOARCH &&
		sys.Name == systemDeploymentName &&
		sys.BinPath == systemDeploymentBinPath
}

func DataplaneDeploymentSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			ContainerImage: &apigen.ContainerImageConfig{Image: internaldeploy.DataplaneImage},
		},
		Runner: apigen.RunnerConfig{
			Container: apigen.ContainerRunnerConfig{
				Command:           []string{"/opendeploy", "dataplane"},
				DisableDataVolume: true,
				Mounts: []*apigen.ContainerMount{{
					Host:      dataplaneStateDir,
					Container: dataplaneStateDir,
					Readonly:  true,
				}},
			},
		},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		},
	}
}

func isDataplaneDeploymentSpec(spec *apigen.DeploymentSpec) bool {
	if spec == nil {
		return false
	}
	ci := spec.Prepare.ContainerImage
	container := spec.Runner.Container
	return ci != nil &&
		ci.Image == internaldeploy.DataplaneImage &&
		len(container.Command) == 2 &&
		container.Command[0] == "/opendeploy" &&
		container.Command[1] == "dataplane" &&
		container.DisableDataVolume &&
		len(container.Mounts) == 1 &&
		container.Mounts[0] != nil &&
		container.Mounts[0].Host == dataplaneStateDir &&
		container.Mounts[0].Container == dataplaneStateDir &&
		container.Mounts[0].Readonly &&
		spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
}

type PrimaryStorage struct {
	*deploymentStore
	userSubs            *pubsubu.PubSub[apigen.User]
	secretStatusSubs    *pubsubu.PubSub[apigen.SecretsStatusResponse]
	secretSubs          *pubsubu.PubSub[apigen.SecretReference]
	secretMetaSubs      *pubsubu.PubSub[apigen.SecretMeta]
	userConfigSubs      *pubsubu.PubSub[apigen.UserConfigReference]
	userConfigValueSubs *pubsubu.PubSub[apigen.UserConfig]
	spaceSubs           *pubsubu.PubSub[apigen.Space]
	assetSubs           *pubsubu.PubSub[apigen.AssetMeta]
	enrollmentSubs      *pubsubu.PubSub[apigen.EnrollmentRequestStatus]
}

func NewPrimaryStorage(dbPath string) *PrimaryStorage {
	db := mustInitPrimary(dbPath)
	return &PrimaryStorage{
		deploymentStore:     newDeploymentStore(db),
		userSubs:            &pubsubu.PubSub[apigen.User]{},
		secretStatusSubs:    &pubsubu.PubSub[apigen.SecretsStatusResponse]{},
		secretSubs:          &pubsubu.PubSub[apigen.SecretReference]{},
		secretMetaSubs:      &pubsubu.PubSub[apigen.SecretMeta]{},
		userConfigSubs:      &pubsubu.PubSub[apigen.UserConfigReference]{},
		userConfigValueSubs: &pubsubu.PubSub[apigen.UserConfig]{},
		spaceSubs:           &pubsubu.PubSub[apigen.Space]{},
		assetSubs:           &pubsubu.PubSub[apigen.AssetMeta]{},
		enrollmentSubs:      &pubsubu.PubSub[apigen.EnrollmentRequestStatus]{},
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

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (s *PrimaryStorage) MustWriteReplicatedDeploymentStatus(st *apigen.DeploymentStatus) {
	if st == nil || st.DeploymentID == 0 || st.UpdatedAt.IsZero() {
		return
	}
	ctx := context.Background()

	s.mu.Lock()
	defer s.mu.Unlock()

	dbID := int64(st.DeploymentID)
	params := statusProtoToInsertParams(dbID, st)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deployment_status_history (
		    deployment_id, updated_at,
		    preparer_config_version, preparer_artifact, preparer_status,
		    runner_config_version, runner_pid, runner_artifact, runner_status,
		    runner_num_restarts, runner_last_restart_at, runner_extra_blob
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(deployment_id, updated_at) DO UPDATE SET
		    preparer_config_version = excluded.preparer_config_version,
		    preparer_artifact = excluded.preparer_artifact,
		    preparer_status = excluded.preparer_status,
		    runner_config_version = excluded.runner_config_version,
		    runner_pid = excluded.runner_pid,
		    runner_artifact = excluded.runner_artifact,
		    runner_status = excluded.runner_status,
		    runner_num_restarts = excluded.runner_num_restarts,
		    runner_last_restart_at = excluded.runner_last_restart_at,
		    runner_extra_blob = excluded.runner_extra_blob`,
		params.DeploymentID, params.UpdatedAt,
		params.PreparerConfigVersion, params.PreparerArtifact, params.PreparerStatus,
		params.RunnerConfigVersion, params.RunnerPid, params.RunnerArtifact, params.RunnerStatus,
		params.RunnerNumRestarts, params.RunnerLastRestartAt, params.RunnerExtraBlob,
	); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatusHistory: %v", err))
	}

	var cached time.Time
	if cur := s.statusCache[st.DeploymentID]; cur != nil {
		cached = cur.UpdatedAt
	}
	if !st.UpdatedAt.Before(cached) {
		if err := q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentStatus: %v", err))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	if !st.UpdatedAt.Before(cached) {
		s.statusCache[st.DeploymentID] = st
		s.notifyFromCache(st.DeploymentID)
	}
}

// --- deployment history ---

func (s *PrimaryStorage) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
	ctx := context.Background()
	dbID := int64(deploymentID)
	rows, err := s.q.ListDeploymentConfigHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentConfigHistory: %v", err))
	}
	// Get the config_id and created_at from cache for display.
	var cid apigen.DeploymentIdentifier
	var createdAt time.Time
	if cfg, ok := s.configCache[deploymentID]; ok {
		cid = cfg.ConfigID
		createdAt = cfg.CreatedAt
	}
	out := make([]*apigen.DeploymentConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, configHistoryRowToProto(dbID, cid, createdAt, r))
	}
	return out
}

func (s *PrimaryStorage) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.DeploymentStatus {
	ctx := context.Background()
	dbID := int64(deploymentID)
	rows, err := s.q.ListDeploymentStatusHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentStatusHistory: %v", err))
	}
	out := make([]*apigen.DeploymentStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusRowToProto(dbID, r))
	}
	return out
}

// --- desired state ---

type DeploymentConfigUpdate struct {
	ExpectedVersion int32
	SpaceID         *int32
	Spec            *apigen.DeploymentSpec
	DesiredState    *apigen.DesiredState
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
		return configRowToProto(existing), false, false
	}

	specBlob := existing.SpecBlob
	if update.Spec != nil {
		specBlob = update.Spec.Encode()
	}
	spaceID := existing.SpaceID
	if update.SpaceID != nil {
		spaceID = int64(*update.SpaceID)
	}
	desiredVersion := existing.DesiredVersion
	desiredRunning := existing.DesiredRunning
	if update.DesiredState != nil {
		desiredVersion = update.DesiredState.Version
		desiredRunning = boolToInt(update.DesiredState.Running)
	}
	deleted := existing.Deleted
	if update.Deleted != nil {
		deleted = boolToInt(*update.Deleted)
	}

	if spaceID == existing.SpaceID &&
		bytes.Equal(specBlob, existing.SpecBlob) &&
		desiredVersion == existing.DesiredVersion &&
		desiredRunning == existing.DesiredRunning &&
		deleted == existing.Deleted {
		if err := tx.Commit(); err != nil {
			panic(fmt.Sprintf("commit: %v", err))
		}
		return configRowToProto(existing), false, true
	}

	params := UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        spaceID,
		Machine:        existing.Machine,
		Name:           existing.Name,
		CreatedAt:      existing.CreatedAt,
		Version:        existing.Version + 1,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: desiredRunning,
		Deleted:        deleted,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        params.Version,
		UpdatedAt:      params.UpdatedAt,
		UpdatedBy:      params.UpdatedBy,
		SpecBlob:       params.SpecBlob,
		DesiredVersion: params.DesiredVersion,
		DesiredRunning: params.DesiredRunning,
		Deleted:        params.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(params)
	s.configCache[deploymentID] = cfg
	s.notifyFromCache(deploymentID)
	return cfg, true, true
}

func (s *PrimaryStorage) MustSetDeploymentDesiredState(ctx apigen.Context, deploymentID int32, desired apigen.DesiredState) {
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

	if err := q.UpdateDesiredState(bgCtx, UpdateDesiredStateParams{
		DesiredVersion: desired.Version,
		DesiredRunning: boolToInt(desired.Running),
		UpdatedAt:      now,
		UpdatedBy:      userID,
		DeploymentID:   dbID,
	}); err != nil {
		panic(fmt.Sprintf("UpdateDesiredState: %v", err))
	}

	updated, err := q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig after update: %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        updated.Version,
		UpdatedAt:      updated.UpdatedAt,
		UpdatedBy:      updated.UpdatedBy,
		SpecBlob:       updated.SpecBlob,
		DesiredVersion: updated.DesiredVersion,
		DesiredRunning: updated.DesiredRunning,
		Deleted:        updated.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = configRowToProto(updated)
	s.notifyFromCache(deploymentID)
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

	var specBlob []byte
	if spec != nil {
		specBlob = spec.Encode()
	}

	newVersion := existing.Version + 1
	params := UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        existing.SpaceID,
		Machine:        existing.Machine,
		Name:           existing.Name,
		CreatedAt:      existing.CreatedAt,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyFromCache(deploymentID)
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
		DeploymentID:   dbID,
		SpaceID:        int64(spaceID),
		Machine:        existing.Machine,
		Name:           existing.Name,
		CreatedAt:      existing.CreatedAt,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       existing.SpecBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       existing.SpecBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyFromCache(deploymentID)
}

// MustCreateDeployment creates a brand-new deployment from a DeploymentIdentifier and spec.
// It allocates a deployment ID, persists the config, inserts a default status,
// and returns the resulting DeploymentConfig.
func (s *PrimaryStorage) MustCreateDeployment(ctx apigen.Context, cid *apigen.DeploymentIdentifier, spec *apigen.DeploymentSpec, desired apigen.DesiredState) *apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reject if a non-deleted deployment with the same identifier already exists.
	for _, cfg := range s.configCache {
		if cfg.ConfigID == *cid && !cfg.Deleted {
			panic(fmt.Sprintf("deployment %d/%s/%s already exists", cid.SpaceID, cid.Machine, cid.Name))
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

	var specBlob []byte
	if spec != nil {
		specBlob = spec.Encode()
	}

	row, err := q.CreateDeploymentConfig(bgCtx, CreateDeploymentConfigParams{
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      now,
		Version:        1,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: desired.Version,
		DesiredRunning: boolToInt(desired.Running),
		Deleted:        0,
	})
	if err != nil {
		panic(fmt.Sprintf("CreateDeploymentConfig: %v", err))
	}
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        1,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: desired.Version,
		DesiredRunning: boolToInt(desired.Running),
		Deleted:        0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (create): %v", err))
	}

	s.insertDefaultStatus(q, dbID)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      row.CreatedAt,
		Version:        1,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: desired.Version,
		DesiredRunning: boolToInt(desired.Running),
		Deleted:        0,
	})
	id := int32(dbID)
	s.configCache[id] = cfg
	s.notifyFromCache(id)
	return cfg
}

// EnsureSystemDeployment creates the OPENDEPLOY opendeploy deployment for
// the given machine if it does not already exist. When opendeployVersion is
// known, first-time system deployments are marked desired-running at that
// version so the systemd runner can observe the already-running service.
func (s *PrimaryStorage) EnsureSystemDeployment(machine string, opendeployVersion string) {
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	cid := apigen.DeploymentIdentifier{
		SpaceID: OpendeploySpaceID,
		Machine: machine,
		Name:    systemDeploymentName,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if it already exists.
	for _, cfg := range s.configCache {
		if cfg.ConfigID == cid && !cfg.Deleted {
			if !isSystemDeploymentSpec(&cfg.Spec) {
				slog.Warn("repairing system deployment spec", "machine", machine, "deploymentID", cfg.ID)
				s.repairSystemDeploymentLocked(cfg.ID)
			}
			return
		}
	}

	spec := SystemDeploymentSpec()
	specBlob := spec.Encode()

	bgCtx := context.Background()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	desiredRunning := int64(0)
	if opendeployVersion != "" {
		desiredRunning = 1
	}

	row, err := q.CreateDeploymentConfig(bgCtx, CreateDeploymentConfigParams{
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      now,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: opendeployVersion,
		DesiredRunning: desiredRunning,
		Deleted:        0,
	})
	if err != nil {
		panic(fmt.Sprintf("CreateDeploymentConfig (system): %v", err))
	}
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: opendeployVersion,
		DesiredRunning: desiredRunning,
		Deleted:        0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (system): %v", err))
	}

	s.insertDefaultStatus(q, dbID)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      row.CreatedAt,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: opendeployVersion,
		DesiredRunning: desiredRunning,
		Deleted:        0,
	})
	s.notifyFromCache(id)
	slog.Info("created system deployment", "machine", machine, "version", opendeployVersion)
}

// EnsureDataplaneDeployment creates or repairs the per-machine opendeploy-net
// internal deployment and returns its config. Creation requires an explicit
// desired version; existing deployments keep their desired version unless they
// were created by an older version without one.
func (s *PrimaryStorage) EnsureDataplaneDeployment(machine string, opendeployVersion string) *apigen.DeploymentConfig {
	desiredVersion := strings.TrimSpace(opendeployVersion)
	if desiredVersion == "" {
		panic("EnsureDataplaneDeployment requires an explicit OpenDeploy version")
	}
	cid := apigen.DeploymentIdentifier{
		SpaceID: OpendeploySpaceID,
		Machine: machine,
		Name:    dataplaneDeploymentName,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cfg := range s.configCache {
		if cfg.ConfigID == cid && !cfg.Deleted {
			if !isDataplaneDeploymentSpec(&cfg.Spec) || cfg.DesiredState.Version == "" {
				slog.Warn("repairing dataplane deployment", "machine", machine, "deploymentID", cfg.ID)
				s.repairDataplaneDeploymentLocked(cfg.ID, desiredVersion)
				cfg = s.configCache[cfg.ID]
			}
			return cfg
		}
	}

	spec := DataplaneDeploymentSpec()
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
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      now,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: 1,
		Deleted:        0,
	})
	if err != nil {
		panic(fmt.Sprintf("CreateDeploymentConfig (dataplane): %v", err))
	}
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: 1,
		Deleted:        0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (dataplane): %v", err))
	}

	s.insertDefaultStatus(q, dbID)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        int64(cid.SpaceID),
		Machine:        cid.Machine,
		Name:           cid.Name,
		CreatedAt:      row.CreatedAt,
		Version:        1,
		UpdatedAt:      now,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: 1,
		Deleted:        0,
	})
	s.notifyFromCache(id)
	slog.Info("created dataplane deployment", "machine", machine, "version", desiredVersion)
	return s.configCache[id]
}

// EnsureDataplaneDeploymentsForSystemDeployments is the startup migration for
// Phase 1 networking. It creates a paired opendeploy-net deployment for every
// existing per-machine opendeploy system deployment, using the primary's current
// release version for newly created dataplanes.
func (s *PrimaryStorage) EnsureDataplaneDeploymentsForSystemDeployments(opendeployVersion string) []*apigen.DeploymentConfig {
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	if opendeployVersion == "" {
		panic("EnsureDataplaneDeploymentsForSystemDeployments requires an explicit OpenDeploy version")
	}

	s.mu.Lock()
	machines := make([]string, 0)
	seen := map[string]bool{}
	for _, cfg := range s.configCache {
		if IsSystemDeploymentConfig(cfg) && !cfg.Deleted && !seen[cfg.ConfigID.Machine] {
			seen[cfg.ConfigID.Machine] = true
			machines = append(machines, cfg.ConfigID.Machine)
		}
	}
	s.mu.Unlock()
	sort.Strings(machines)

	out := make([]*apigen.DeploymentConfig, 0, len(machines))
	for _, machine := range machines {
		out = append(out, s.EnsureDataplaneDeployment(machine, opendeployVersion))
	}
	return out
}

func (s *PrimaryStorage) repairSystemDeploymentLocked(deploymentID int32) {
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
		panic(fmt.Sprintf("GetDeploymentConfig (system repair): %v", err))
	}
	specBlob := SystemDeploymentSpec().Encode()
	newVersion := existing.Version + 1
	params := UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        existing.SpaceID,
		Machine:        existing.Machine,
		Name:           existing.Name,
		CreatedAt:      existing.CreatedAt,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      0,
		SpecBlob:       specBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig (system repair): %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      0,
		SpecBlob:       specBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (system repair): %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[deploymentID] = upsertParamsToProto(params)
	s.notifyFromCache(deploymentID)
}

func (s *PrimaryStorage) repairDataplaneDeploymentLocked(deploymentID int32, desiredVersion string) {
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
		panic(fmt.Sprintf("GetDeploymentConfig (dataplane repair): %v", err))
	}
	if existing.DesiredVersion != "" {
		desiredVersion = existing.DesiredVersion
	}
	specBlob := DataplaneDeploymentSpec().Encode()
	newVersion := existing.Version + 1
	params := UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SpaceID:        existing.SpaceID,
		Machine:        existing.Machine,
		Name:           existing.Name,
		CreatedAt:      existing.CreatedAt,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      0,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: 1,
		Deleted:        existing.Deleted,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig (dataplane repair): %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      0,
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: 1,
		Deleted:        existing.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (dataplane repair): %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}
	cfg := configRowToProto(DeploymentConfig{
		DeploymentID:   dbID,
		SpaceID:        params.SpaceID,
		Machine:        params.Machine,
		Name:           params.Name,
		CreatedAt:      params.CreatedAt,
		Version:        params.Version,
		UpdatedAt:      params.UpdatedAt,
		UpdatedBy:      params.UpdatedBy,
		SpecBlob:       params.SpecBlob,
		DesiredVersion: params.DesiredVersion,
		DesiredRunning: params.DesiredRunning,
		Deleted:        params.Deleted,
	})
	s.configCache[deploymentID] = cfg
	s.notifyFromCache(deploymentID)
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

func statusRowToProto(dbID int64, r DeploymentStatusHistory) *apigen.DeploymentStatus {
	st := &apigen.DeploymentStatus{
		UpdatedAt:    nanosToClock(r.UpdatedAt),
		DeploymentID: int32(dbID),
	}
	if r.PreparerStatus.Valid {
		st.Preparer = apigen.PreparerStatus{
			DeploymentConfigVersion: int32(r.PreparerConfigVersion.Int64),
			Artifact:                r.PreparerArtifact.String,
			Status:                  apigen.PreparationStatus(r.PreparerStatus.Int64),
		}
	}
	if r.RunnerStatus.Valid {
		st.Runner = apigen.RunnerStatus{
			DeploymentConfigVersion: int32(r.RunnerConfigVersion.Int64),
			RunningPid:              int32(r.RunnerPid.Int64),
			RunningArtifact:         r.RunnerArtifact.String,
			Status:                  apigen.RunningStatus(r.RunnerStatus.Int64),
			NumberOfRestarts:        int32(r.RunnerNumRestarts.Int64),
		}
		if r.RunnerLastRestartAt.Valid {
			st.Runner.LastRestartAt = time.UnixMilli(r.RunnerLastRestartAt.Int64)
		}
		if len(r.RunnerExtraBlob) > 0 {
			extra, err := apigen.DecodeRunnerStatus(r.RunnerExtraBlob)
			if err != nil {
				slog.Warn("decoding runner status extra blob", "deploymentID", dbID, "err", err)
			} else {
				st.Runner.Endpoints = extra.Endpoints
				st.Runner.NetworkDiagnostics = extra.NetworkDiagnostics
			}
		}
	}
	return st
}

func statusProtoToInsertParams(dbID int64, st *apigen.DeploymentStatus) InsertDeploymentStatusHistoryParams {
	p := InsertDeploymentStatusHistoryParams{
		DeploymentID:    dbID,
		UpdatedAt:       clockToNanos(st.UpdatedAt),
		RunnerExtraBlob: []byte{},
	}
	if !st.Preparer.IsZero() {
		p.PreparerConfigVersion = sql.NullInt64{Int64: int64(st.Preparer.DeploymentConfigVersion), Valid: true}
		p.PreparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		p.PreparerStatus = sql.NullInt64{Int64: int64(st.Preparer.Status), Valid: true}
	}
	if !st.Runner.IsZero() {
		p.RunnerConfigVersion = sql.NullInt64{Int64: int64(st.Runner.DeploymentConfigVersion), Valid: true}
		p.RunnerPid = sql.NullInt64{Int64: int64(st.Runner.RunningPid), Valid: true}
		p.RunnerArtifact = sql.NullString{String: st.Runner.RunningArtifact, Valid: true}
		p.RunnerStatus = sql.NullInt64{Int64: int64(st.Runner.Status), Valid: true}
		p.RunnerNumRestarts = sql.NullInt64{Int64: int64(st.Runner.NumberOfRestarts), Valid: true}
		if !st.Runner.LastRestartAt.IsZero() {
			p.RunnerLastRestartAt = sql.NullInt64{Int64: st.Runner.LastRestartAt.UnixMilli(), Valid: true}
		}
		p.RunnerExtraBlob = runnerStatusExtraBlob(st.Runner)
	}
	return p
}

func runnerStatusExtraBlob(r apigen.RunnerStatus) []byte {
	if len(r.Endpoints) == 0 && len(r.NetworkDiagnostics) == 0 {
		return []byte{}
	}
	return (&apigen.RunnerStatus{
		Endpoints:          r.Endpoints,
		NetworkDiagnostics: r.NetworkDiagnostics,
	}).Encode()
}

func statusInsertToUpsert(p InsertDeploymentStatusHistoryParams) UpsertDeploymentStatusParams {
	return UpsertDeploymentStatusParams{
		DeploymentID:          p.DeploymentID,
		UpdatedAt:             p.UpdatedAt,
		PreparerConfigVersion: p.PreparerConfigVersion,
		PreparerArtifact:      p.PreparerArtifact,
		PreparerStatus:        p.PreparerStatus,
		RunnerConfigVersion:   p.RunnerConfigVersion,
		RunnerPid:             p.RunnerPid,
		RunnerArtifact:        p.RunnerArtifact,
		RunnerStatus:          p.RunnerStatus,
		RunnerNumRestarts:     p.RunnerNumRestarts,
		RunnerLastRestartAt:   p.RunnerLastRestartAt,
		RunnerExtraBlob:       p.RunnerExtraBlob,
	}
}

func statusToHistory(s DeploymentStatus) DeploymentStatusHistory {
	return DeploymentStatusHistory{
		DeploymentID:          s.DeploymentID,
		UpdatedAt:             s.UpdatedAt,
		PreparerConfigVersion: s.PreparerConfigVersion,
		PreparerArtifact:      s.PreparerArtifact,
		PreparerStatus:        s.PreparerStatus,
		RunnerConfigVersion:   s.RunnerConfigVersion,
		RunnerPid:             s.RunnerPid,
		RunnerArtifact:        s.RunnerArtifact,
		RunnerStatus:          s.RunnerStatus,
		RunnerNumRestarts:     s.RunnerNumRestarts,
		RunnerLastRestartAt:   s.RunnerLastRestartAt,
		RunnerExtraBlob:       s.RunnerExtraBlob,
	}
}

func configHistoryRowToProto(dbID int64, cid apigen.DeploymentIdentifier, createdAt time.Time, r DeploymentConfigHistory) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(r.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", dbID, "version", r.Version, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID:        int32(dbID),
		ConfigID:  cid,
		CreatedAt: createdAt,
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      ptru.SafeDref(spec),
		DesiredState: apigen.DesiredState{
			Version: r.DesiredVersion,
			Running: r.DesiredRunning != 0,
		},
		Deleted: r.Deleted != 0,
	}
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
		DeploymentID:   int64(cfg.ID),
		SpaceID:        int64(cfg.ConfigID.SpaceID),
		Machine:        cfg.ConfigID.Machine,
		Name:           cfg.ConfigID.Name,
		CreatedAt:      timeToMillis(cfg.CreatedAt),
		Version:        int64(cfg.Version),
		UpdatedAt:      cfg.UpdatedAt.UnixMilli(),
		UpdatedBy:      int64(cfg.UpdatedBy),
		SpecBlob:       specBlob,
		DesiredVersion: cfg.DesiredState.Version,
		DesiredRunning: boolToInt(cfg.DesiredState.Running),
		Deleted:        boolToInt(cfg.Deleted),
	}
}

func configRowToProto(r DeploymentConfig) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(r.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", r.DeploymentID, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID: int32(r.DeploymentID),
		ConfigID: apigen.DeploymentIdentifier{
			SpaceID: int32(r.SpaceID),
			Machine: r.Machine,
			Name:    r.Name,
		},
		CreatedAt: millisToTime(r.CreatedAt),
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      ptru.SafeDref(spec),
		DesiredState: apigen.DesiredState{
			Version: r.DesiredVersion,
			Running: r.DesiredRunning != 0,
		},
		Deleted: r.Deleted != 0,
	}
}

func upsertParamsToProto(p UpsertDeploymentConfigParams) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(p.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", p.DeploymentID, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID: int32(p.DeploymentID),
		ConfigID: apigen.DeploymentIdentifier{
			SpaceID: int32(p.SpaceID),
			Machine: p.Machine,
			Name:    p.Name,
		},
		CreatedAt: millisToTime(p.CreatedAt),
		Version:   int32(p.Version),
		UpdatedAt: time.UnixMilli(p.UpdatedAt),
		UpdatedBy: int32(p.UpdatedBy),
		Spec:      ptru.SafeDref(spec),
		DesiredState: apigen.DesiredState{
			Version: p.DesiredVersion,
			Running: p.DesiredRunning != 0,
		},
		Deleted: p.Deleted != 0,
	}
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

func (s *PrimaryStorage) UserCount() int {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("UserCount: %v", err))
	}
	return len(rows)
}

func (s *PrimaryStorage) FetchLatestOpenDeployConfig() (OpendeployConfig, error) {
	r, err := s.q.GetLatestConfig(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return OpendeployConfig{}, ErrNotFound
	}
	return r, err
}

func (s *PrimaryStorage) AppendOpenDeploySettings(blob []byte) (int64, error) {
	res, err := s.db.ExecContext(context.Background(), `
INSERT INTO opendeploy_config (updated_at, config_blob) VALUES (?, ?)
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
