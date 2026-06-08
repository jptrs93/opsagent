package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SystemEnvironment is the reserved environment name for OpenDeploy's own
// self-management deployments. It is auto-created for each machine and
// excluded from the user-config deletion sweep.
const SystemEnvironment = "OPENDEPLOY_SYSTEM"

type StorageAdapter struct {
	*deploymentStore
	userSubs *pubsubu.PubSub[apigen.User]
}

func NewStorageAdapter(dbPath string) *StorageAdapter {
	db := mustInitPrimary(dbPath)
	return &StorageAdapter{
		deploymentStore: newDeploymentStore(db),
		userSubs:        &pubsubu.PubSub[apigen.User]{},
	}
}

// ListActiveDeploymentConfigs returns all non-deleted configs from the cache.
func (s *StorageAdapter) ListActiveDeploymentConfigs() []*apigen.DeploymentConfig {
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

func (s *StorageAdapter) MustWriteReplicatedDeploymentStatus(st *apigen.DeploymentStatus) {
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
		    runner_num_restarts, runner_last_restart_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(deployment_id, updated_at) DO UPDATE SET
		    preparer_config_version = excluded.preparer_config_version,
		    preparer_artifact = excluded.preparer_artifact,
		    preparer_status = excluded.preparer_status,
		    runner_config_version = excluded.runner_config_version,
		    runner_pid = excluded.runner_pid,
		    runner_artifact = excluded.runner_artifact,
		    runner_status = excluded.runner_status,
		    runner_num_restarts = excluded.runner_num_restarts,
		    runner_last_restart_at = excluded.runner_last_restart_at`,
		params.DeploymentID, params.UpdatedAt,
		params.PreparerConfigVersion, params.PreparerArtifact, params.PreparerStatus,
		params.RunnerConfigVersion, params.RunnerPid, params.RunnerArtifact, params.RunnerStatus,
		params.RunnerNumRestarts, params.RunnerLastRestartAt,
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

func (s *StorageAdapter) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
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

func (s *StorageAdapter) MustFetchDeploymentStatusHistory(deploymentID int32) []*apigen.DeploymentStatus {
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

func (s *StorageAdapter) MustSetDeploymentDesiredState(ctx apigen.Context, deploymentID int32, desired apigen.DesiredState) {
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

func (s *StorageAdapter) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
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
		Environment:    existing.Environment,
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

// MustCreateDeployment creates a brand-new deployment from a DeploymentIdentifier and spec.
// It allocates a deployment ID, persists the config, inserts a default status,
// and returns the resulting DeploymentConfig.
func (s *StorageAdapter) MustCreateDeployment(ctx apigen.Context, cid *apigen.DeploymentIdentifier, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reject if a non-deleted deployment with the same identifier already exists.
	for _, cfg := range s.configCache {
		if cfg.ConfigID == *cid && !cfg.Deleted {
			panic(fmt.Sprintf("deployment %s/%s/%s already exists", cid.Environment, cid.Machine, cid.Name))
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
		Environment: cid.Environment,
		Machine:     cid.Machine,
		Name:        cid.Name,
		CreatedAt:   now,
		Version:     1,
		UpdatedAt:   now,
		UpdatedBy:   userID,
		SpecBlob:    specBlob,
		Deleted:     0,
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
		SpecBlob:     specBlob,
		Deleted:      0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (create): %v", err))
	}

	s.insertDefaultStatus(q, dbID)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		Environment:  cid.Environment,
		Machine:      cid.Machine,
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
	s.notifyFromCache(id)
	return cfg
}

// EnsureSystemDeployment creates the OPENDEPLOY_SYSTEM opendeploy deployment for
// the given machine if it does not already exist.
func (s *StorageAdapter) EnsureSystemDeployment(machine string) {
	cid := apigen.DeploymentIdentifier{
		Environment: SystemEnvironment,
		Machine:     machine,
		Name:        "opendeploy",
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if it already exists.
	for _, cfg := range s.configCache {
		if cfg.ConfigID == cid && !cfg.Deleted {
			return
		}
	}

	spec := &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{
			GithubRelease: apigen.GithubReleaseConfig{
				Repo: "github.com/jptrs93/opsagent",
			},
		},
		Runner: apigen.RunnerConfig{
			Systemd: apigen.SystemdRunnerConfig{
				Name:    "opendeploy",
				BinPath: "/var/lib/opendeploy/bin/opendeploy",
			},
		},
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
		Environment: cid.Environment,
		Machine:     cid.Machine,
		Name:        cid.Name,
		CreatedAt:   now,
		Version:     1,
		UpdatedAt:   now,
		SpecBlob:    specBlob,
		Deleted:     0,
	})
	if err != nil {
		panic(fmt.Sprintf("CreateDeploymentConfig (system): %v", err))
	}
	dbID := row.DeploymentID

	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID: dbID,
		Version:      1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (system): %v", err))
	}

	s.insertDefaultStatus(q, dbID)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		Environment:  cid.Environment,
		Machine:      cid.Machine,
		Name:         cid.Name,
		CreatedAt:    row.CreatedAt,
		Version:      1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	})
	s.notifyFromCache(id)
	slog.Info("created system deployment", "machine", machine)
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
	}
	return st
}

func statusProtoToInsertParams(dbID int64, st *apigen.DeploymentStatus) InsertDeploymentStatusHistoryParams {
	p := InsertDeploymentStatusHistoryParams{
		DeploymentID: dbID,
		UpdatedAt:    clockToNanos(st.UpdatedAt),
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
	}
	return p
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
		Environment:    cfg.ConfigID.Environment,
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
			Environment: r.Environment,
			Machine:     r.Machine,
			Name:        r.Name,
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
			Environment: p.Environment,
			Machine:     p.Machine,
			Name:        p.Name,
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

// --- auth: users ---

var ErrNotFound = fmt.Errorf("not found")

func (s *StorageAdapter) WriteUser(user *apigen.InternalUser) {
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

func (s *StorageAdapter) ListUsersPublic() []*apigen.User {
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

func (s *StorageAdapter) SubscribeUserUpdates() (*pubsubu.Sub[apigen.User], func()) {
	sub := s.userSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *StorageAdapter) FetchUserByID(id int32) (*apigen.InternalUser, error) {
	row, err := s.q.GetUser(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return apigen.DecodeInternalUser(row.DataBlob)
}

func (s *StorageAdapter) FetchUserMatching(predicate func(*apigen.InternalUser) bool) (*apigen.InternalUser, error) {
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

func (s *StorageAdapter) UpdateUserMatching(predicate func(*apigen.InternalUser) bool, f func(*apigen.InternalUser)) {
	user, err := s.FetchUserMatching(predicate)
	if err != nil {
		panic(fmt.Sprintf("UpdateUserMatching: %v", err))
	}
	f(user)
	s.WriteUser(user)
}

func (s *StorageAdapter) UserCount() int {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		panic(fmt.Sprintf("UserCount: %v", err))
	}
	return len(rows)
}

func (s *StorageAdapter) FetchMasterPasswordHash() (hash string, dbConfigured bool, err error) {
	row, err := s.q.GetAuthSettings(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.MasterPasswordHash, true, nil
}

func (s *StorageAdapter) SetMasterPasswordHash(hash string) error {
	return s.q.UpsertAuthSettings(context.Background(), hash)
}

// --- auth: public keys ---
func (s *StorageAdapter) WritePublicKey(rec *apigen.PublicKeyRecord) {
	ctx := context.Background()
	if err := s.q.UpsertPublicKey(ctx, UpsertPublicKeyParams{
		Kid:      rec.Kid,
		KeyBytes: rec.KeyBytes,
	}); err != nil {
		panic(fmt.Sprintf("UpsertPublicKey: %v", err))
	}
}

func (s *StorageAdapter) FetchPublicKey(kid string) (*apigen.PublicKeyRecord, error) {
	row, err := s.q.GetPublicKey(context.Background(), kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &apigen.PublicKeyRecord{Kid: row.Kid, KeyBytes: row.KeyBytes}, nil
}
