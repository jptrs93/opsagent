package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logstore"
)

// SystemEnvironment is the reserved environment name for opsagent's own
// self-management deployments. It is auto-created for each machine and
// excluded from the user-config deletion sweep.
const SystemEnvironment = "OPSAGENT_SYSTEM"

type StorageAdapter struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	configCache map[int32]*apigen.DeploymentConfig
	statusCache map[int32]*apigen.DeploymentStatus
	subs        *logstore.Subs[apigen.DeploymentWithStatus]
	userSubs    *logstore.Subs[apigen.User]
}

func NewStorageAdapter(dbPath string) *StorageAdapter {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	s := &StorageAdapter{
		db:          db,
		q:           New(db),
		configCache: make(map[int32]*apigen.DeploymentConfig),
		statusCache: make(map[int32]*apigen.DeploymentStatus),
		subs:        &logstore.Subs[apigen.DeploymentWithStatus]{},
		userSubs:    &logstore.Subs[apigen.User]{},
	}
	s.loadCache()
	return s
}

func (s *StorageAdapter) loadCache() {
	ctx := context.Background()
	rows, err := s.q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListAllDeploymentConfigs: %v", err))
	}
	for _, row := range rows {
		id := int32(row.DeploymentID)
		s.configCache[id] = configRowToProto(row)
	}

	statuses, err := s.q.ListAllDeploymentStatuses(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListAllDeploymentStatuses: %v", err))
	}
	for _, st := range statuses {
		id := int32(st.DeploymentID)
		s.statusCache[id] = statusRowToProto(st.DeploymentID, statusToHistory(st))
	}

	// Ensure every config has a status entry (invariant: status is never nil).
	now := time.Now().UnixMilli()
	for id := range s.configCache {
		if _, ok := s.statusCache[id]; ok {
			continue
		}
		st := &apigen.DeploymentStatus{
			StatusSeqNo:  0,
			Timestamp:    time.UnixMilli(now),
			DeploymentID: id,
		}
		params := statusProtoToInsertParams(int64(id), st)
		if err := s.q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
			panic(fmt.Sprintf("loadCache: UpsertDeploymentStatus (default): %v", err))
		}
		if err := s.q.InsertDeploymentStatusHistory(ctx, params); err != nil {
			panic(fmt.Sprintf("loadCache: InsertDeploymentStatusHistory (default): %v", err))
		}
		s.statusCache[id] = st
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

// resolveDeploymentID looks up or creates a deployment_identifiers row,
// returning the internal integer ID. Only used at config-save time.
func (s *StorageAdapter) resolveDeploymentID(ctx context.Context, tx *sql.Tx, cid *apigen.DeploymentIdentifier) (int64, error) {
	// Check cache first.
	for id, cfg := range s.configCache {
		if cfg.ConfigID != nil && *cfg.ConfigID == *cid {
			return int64(id), nil
		}
	}
	q := s.q.WithTx(tx)
	dbID, err := q.UpsertDeploymentID(ctx, UpsertDeploymentIDParams{
		Environment: cid.Environment,
		Machine:     cid.Machine,
		Name:        cid.Name,
		CreatedAt:   time.Now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	return dbID, nil
}

func (s *StorageAdapter) mustResolveDeploymentID(ctx context.Context, tx *sql.Tx, cid *apigen.DeploymentIdentifier) int64 {
	dbID, err := s.resolveDeploymentID(ctx, tx, cid)
	if err != nil {
		panic(fmt.Sprintf("resolveDeploymentID: %v", err))
	}
	return dbID
}

// --- PrimaryLocalStore: OperatorStore ---

func (s *StorageAdapter) MustWriteDeploymentStatus(ctx context.Context, deploymentID int32, f func(*apigen.DeploymentStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.statusCache[deploymentID]
	if current == nil {
		current = &apigen.DeploymentStatus{DeploymentID: deploymentID}
	}

	f(current)

	dbID := int64(deploymentID)
	params := statusProtoToInsertParams(dbID, current)
	if err := s.q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus: %v", err))
	}
	if err := s.q.InsertDeploymentStatusHistory(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatusHistory: %v", err))
	}

	s.statusCache[deploymentID] = current
	s.notifyFromCache(deploymentID)
}

func (s *StorageAdapter) MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var snapshot []apigen.DeploymentWithStatus
	for id, cfg := range s.configCache {
		if machine != "" && (cfg.ConfigID == nil || cfg.ConfigID.Machine != machine) {
			continue
		}
		if cfg.Deleted {
			continue
		}
		snapshot = append(snapshot, apigen.DeploymentWithStatus{
			Config: cfg,
			Status: s.statusCache[id],
		})
	}

	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ConfigID != nil && dws.Config.ConfigID.Machine == machine
		}
	}
	sub, _ := s.subs.Subscribe(filter)
	return snapshot, sub.Ch
}

// --- PrimaryLocalStore: deployment history ---

func (s *StorageAdapter) MustFetchDeploymentHistory(deploymentID int32) []*apigen.DeploymentConfig {
	ctx := context.Background()
	dbID := int64(deploymentID)
	rows, err := s.q.ListDeploymentConfigHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentConfigHistory: %v", err))
	}
	// Get the config_id from cache for display.
	var cid *apigen.DeploymentIdentifier
	if cfg, ok := s.configCache[deploymentID]; ok {
		cid = cfg.ConfigID
	}
	out := make([]*apigen.DeploymentConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, configHistoryRowToProto(dbID, cid, r))
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

// --- PrimaryLocalStore: desired state ---

func (s *StorageAdapter) MustSetDeploymentDesiredState(ctx apigen.Context, deploymentID int32, desired apigen.DesiredState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	if err := s.q.UpdateDesiredState(bgCtx, UpdateDesiredStateParams{
		DesiredVersion: desired.Version,
		DesiredRunning: boolToInt(desired.Running),
		UpdatedAt:      now,
		UpdatedBy:      userID,
		DeploymentID:   dbID,
	}); err != nil {
		panic(fmt.Sprintf("UpdateDesiredState: %v", err))
	}

	updated, err := s.q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig after update: %v", err))
	}
	if err := s.q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		Version:          updated.Version,
		UpdatedAt:      updated.UpdatedAt,
		UpdatedBy:      updated.UpdatedBy,
		SpecBlob:       updated.SpecBlob,
		DesiredVersion: updated.DesiredVersion,
		DesiredRunning: updated.DesiredRunning,
		Deleted:        updated.Deleted,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
	}

	s.configCache[deploymentID] = configDBRowToProto(updated)
	s.notifyFromCache(deploymentID)
}

// --- PrimaryLocalStore: deployment spec update ---

func (s *StorageAdapter) MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bgCtx := context.Background()
	dbID := int64(deploymentID)
	now := time.Now().UnixMilli()

	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	existing, err := s.q.GetDeploymentConfig(bgCtx, dbID)
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
		Version:        newVersion,
		UpdatedAt:      now,
		UpdatedBy:      userID,
		SpecBlob:       specBlob,
		DesiredVersion: existing.DesiredVersion,
		DesiredRunning: existing.DesiredRunning,
		Deleted:        existing.Deleted,
	}
	if err := s.q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}
	if err := s.q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
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
		if cfg.ConfigID != nil && *cfg.ConfigID == *cid && !cfg.Deleted {
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
	dbID := s.mustResolveDeploymentID(bgCtx, tx, cid)

	var specBlob []byte
	if spec != nil {
		specBlob = spec.Encode()
	}

	params := UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		Environment:  cid.Environment,
		Machine:      cid.Machine,
		Name:         cid.Name,
		Version:      1,
		UpdatedAt:    now,
		UpdatedBy:    userID,
		SpecBlob:     specBlob,
		Deleted:      0,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig (create): %v", err))
	}
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

	s.insertDefaultStatus(bgCtx, q, dbID, now)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	cfg := upsertParamsToProto(params)
	id := int32(dbID)
	s.configCache[id] = cfg
	s.notifyFromCache(id)
	return cfg
}

func (s *StorageAdapter) insertDefaultStatus(ctx context.Context, q *Queries, dbID int64, now int64) {
	id := int32(dbID)
	st := &apigen.DeploymentStatus{
		StatusSeqNo:  0,
		Timestamp:    time.UnixMilli(now),
		DeploymentID: id,
	}
	params := statusProtoToInsertParams(dbID, st)
	if err := q.UpsertDeploymentStatus(ctx, statusInsertToUpsert(params)); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus (default): %v", err))
	}
	if err := q.InsertDeploymentStatusHistory(ctx, params); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatusHistory (default): %v", err))
	}
	s.statusCache[id] = st
}

// EnsureSystemDeployment creates the OPSAGENT_SYSTEM opsagent deployment for
// the given machine if it does not already exist.
func (s *StorageAdapter) EnsureSystemDeployment(machine string) {
	cid := apigen.DeploymentIdentifier{
		Environment: SystemEnvironment,
		Machine:     machine,
		Name:        "opsagent",
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if it already exists.
	for _, cfg := range s.configCache {
		if cfg.ConfigID != nil && *cfg.ConfigID == cid && !cfg.Deleted {
			return
		}
	}

	spec := &apigen.DeploymentSpec{
		Prepare: &apigen.PrepareConfig{
			GithubRelease: &apigen.GithubReleaseConfig{
				Repo: "github.com/jptrs93/opsagent",
			},
		},
		Runner: &apigen.RunnerConfig{
			Systemd: &apigen.SystemdRunnerConfig{
				Name:    "opsagent",
				BinPath: "/var/lib/opsagent/bin/opsagent",
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
	dbID := s.mustResolveDeploymentID(bgCtx, tx, &cid)
	now := time.Now().UnixMilli()

	params := UpsertDeploymentConfigParams{
		DeploymentID: dbID,
		Environment:  cid.Environment,
		Machine:      cid.Machine,
		Name:         cid.Name,
		Version:        1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig (system): %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID: dbID,
		Version:        1,
		UpdatedAt:    now,
		SpecBlob:     specBlob,
		Deleted:      0,
	}); err != nil {
		panic(fmt.Sprintf("InsertDeploymentConfigHistory (system): %v", err))
	}

	s.insertDefaultStatus(bgCtx, q, dbID, now)

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	id := int32(dbID)
	s.configCache[id] = upsertParamsToProto(params)
	s.notifyFromCache(id)
	slog.Info("created system deployment", "machine", machine)
}

// SubscribeDeploymentUpdates returns a channel of deployment changes filtered
// by machine, along with an unsubscribe function.
func (s *StorageAdapter) SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func()) {
	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ConfigID != nil && dws.Config.ConfigID.Machine == machine
		}
	}
	sub, unsub := s.subs.Subscribe(filter)
	return sub.Ch, unsub
}

// FetchDeploymentStatus returns the cached status for a deployment, or nil.
func (s *StorageAdapter) FetchDeploymentStatus(deploymentID int32) *apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCache[deploymentID]
}

func (s *StorageAdapter) notifyFromCache(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	st := s.statusCache[id]
	name := ""
	if cfg.ConfigID != nil {
		name = fmt.Sprintf("%s:%s:%s", cfg.ConfigID.Environment, cfg.ConfigID.Machine, cfg.ConfigID.Name)
	}
	slog.Info("store: notifyFromCache",
		"id", id,
		"name", name,
		"configSeqNo", cfg.Version,
		"hasPreparer", st != nil && st.Preparer != nil,
		"hasRunner", st != nil && st.Runner != nil,
	)
	s.subs.Notify(apigen.DeploymentWithStatus{
		Config: cfg,
		Status: st,
	})
}

// --- row <-> proto conversions ---

func statusRowToProto(dbID int64, r DeploymentStatusHistory) *apigen.DeploymentStatus {
	st := &apigen.DeploymentStatus{
		StatusSeqNo:  int32(r.StatusSeqNo),
		Timestamp:    time.UnixMilli(r.Timestamp),
		DeploymentID: int32(dbID),
	}
	if r.PreparerStatus.Valid {
		st.Preparer = &apigen.PreparerStatus{
			DeploymentConfigVersion: int32(r.PreparerConfigVersion.Int64),
			Artifact:        r.PreparerArtifact.String,
			Status:          apigen.PreparationStatus(r.PreparerStatus.Int64),
		}
	}
	if r.RunnerStatus.Valid {
		st.Runner = &apigen.RunnerStatus{
			DeploymentConfigVersion:  int32(r.RunnerConfigVersion.Int64),
			RunningPid:       int32(r.RunnerPid.Int64),
			RunningArtifact:  r.RunnerArtifact.String,
			Status:           apigen.RunningStatus(r.RunnerStatus.Int64),
			NumberOfRestarts: int32(r.RunnerNumRestarts.Int64),
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
		StatusSeqNo:  int64(st.StatusSeqNo),
		Timestamp:    st.Timestamp.UnixMilli(),
	}
	if st.Preparer != nil {
		p.PreparerConfigVersion = sql.NullInt64{Int64: int64(st.Preparer.DeploymentConfigVersion), Valid: true}
		p.PreparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		p.PreparerStatus = sql.NullInt64{Int64: int64(st.Preparer.Status), Valid: true}
	}
	if st.Runner != nil {
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
		DeploymentID:        p.DeploymentID,
		StatusSeqNo:         p.StatusSeqNo,
		Timestamp:           p.Timestamp,
		PreparerConfigVersion:       p.PreparerConfigVersion,
		PreparerArtifact:    p.PreparerArtifact,
		PreparerStatus:      p.PreparerStatus,
		RunnerConfigVersion:         p.RunnerConfigVersion,
		RunnerPid:           p.RunnerPid,
		RunnerArtifact:      p.RunnerArtifact,
		RunnerStatus:        p.RunnerStatus,
		RunnerNumRestarts:   p.RunnerNumRestarts,
		RunnerLastRestartAt: p.RunnerLastRestartAt,
	}
}

func statusToHistory(s DeploymentStatus) DeploymentStatusHistory {
	return DeploymentStatusHistory{
		DeploymentID:        s.DeploymentID,
		StatusSeqNo:         s.StatusSeqNo,
		Timestamp:           s.Timestamp,
		PreparerConfigVersion:       s.PreparerConfigVersion,
		PreparerArtifact:    s.PreparerArtifact,
		PreparerStatus:      s.PreparerStatus,
		RunnerConfigVersion:         s.RunnerConfigVersion,
		RunnerPid:           s.RunnerPid,
		RunnerArtifact:      s.RunnerArtifact,
		RunnerStatus:        s.RunnerStatus,
		RunnerNumRestarts:   s.RunnerNumRestarts,
		RunnerLastRestartAt: s.RunnerLastRestartAt,
	}
}

func configHistoryRowToProto(dbID int64, cid *apigen.DeploymentIdentifier, r DeploymentConfigHistory) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(r.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", dbID, "version", r.Version, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID:       int32(dbID),
		ConfigID: cid,
		Version:    int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      spec,
		DesiredState: &apigen.DesiredState{
			Version: r.DesiredVersion,
			Running: r.DesiredRunning != 0,
		},
		Deleted: r.Deleted != 0,
	}
}

func configRowToProto(r DeploymentConfig) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(r.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", r.DeploymentID, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID: int32(r.DeploymentID),
		ConfigID: &apigen.DeploymentIdentifier{
			Environment: r.Environment,
			Machine:     r.Machine,
			Name:        r.Name,
		},
		Version:     int32(r.Version),
		UpdatedAt:  time.UnixMilli(r.UpdatedAt),
		UpdatedBy:  int32(r.UpdatedBy),
		Spec:       spec,
		DesiredState: &apigen.DesiredState{
			Version: r.DesiredVersion,
			Running: r.DesiredRunning != 0,
		},
		Deleted: r.Deleted != 0,
	}
}

func configDBRowToProto(r DeploymentConfig) *apigen.DeploymentConfig {
	spec, err := apigen.DecodeDeploymentSpec(r.SpecBlob)
	if err != nil {
		slog.Error("failed decoding deployment spec", "deploymentID", r.DeploymentID, "err", err)
	}
	return &apigen.DeploymentConfig{
		ID: int32(r.DeploymentID),
		ConfigID: &apigen.DeploymentIdentifier{
			Environment: r.Environment,
			Machine:     r.Machine,
			Name:        r.Name,
		},
		Version:     int32(r.Version),
		UpdatedAt:  time.UnixMilli(r.UpdatedAt),
		UpdatedBy:  int32(r.UpdatedBy),
		Spec:       spec,
		DesiredState: &apigen.DesiredState{
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
		ConfigID: &apigen.DeploymentIdentifier{
			Environment: p.Environment,
			Machine:     p.Machine,
			Name:        p.Name,
		},
		Version:     int32(p.Version),
		UpdatedAt:  time.UnixMilli(p.UpdatedAt),
		UpdatedBy:  int32(p.UpdatedBy),
		Spec:       spec,
		DesiredState: &apigen.DesiredState{
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

func (s *StorageAdapter) SubscribeUserUpdates() (*logstore.Sub[apigen.User], func()) {
	return s.userSubs.Subscribe(nil)
}

func (s *StorageAdapter) FetchUserByID(id int32) (*apigen.InternalUser, error) {
	row, err := s.q.GetUser(context.Background(), int64(id))
	if err == sql.ErrNoRows {
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
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &apigen.PublicKeyRecord{Kid: row.Kid, KeyBytes: row.KeyBytes}, nil
}

//go:embed sql/schema.sql
var schema string
