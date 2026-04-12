package sqlite

import (
	"bytes"
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

type StorageAdapter struct {
	db *sql.DB
	q  *Queries

	mu sync.Mutex

	dbIDCache   map[apigen.DeploymentIdentifier]int64
	configCache map[apigen.DeploymentIdentifier]*apigen.DeploymentConfig
	statusCache map[apigen.DeploymentIdentifier]*apigen.DeploymentStatus
	subs     *logstore.Subs[apigen.DeploymentWithStatus]
	userSubs *logstore.Subs[apigen.User]
}

func NewStorageAdapter(dbPath string) *StorageAdapter {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	s := &StorageAdapter{
		db:          db,
		q:           New(db),
		dbIDCache:   make(map[apigen.DeploymentIdentifier]int64),
		configCache: make(map[apigen.DeploymentIdentifier]*apigen.DeploymentConfig),
		statusCache: make(map[apigen.DeploymentIdentifier]*apigen.DeploymentStatus),
		subs:     &logstore.Subs[apigen.DeploymentWithStatus]{},
		userSubs: &logstore.Subs[apigen.User]{},
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
		id := apigen.DeploymentIdentifier{
			Environment: row.Environment,
			Machine:     row.Machine,
			Name:        row.Name,
		}
		s.dbIDCache[id] = row.DeploymentID
		s.configCache[id] = configRowToProto(&id, row)
	}

	statuses, err := s.q.ListLatestDeploymentStatuses(ctx)
	if err != nil {
		panic(fmt.Sprintf("loadCache: ListLatestDeploymentStatuses: %v", err))
	}
	idByDBID := make(map[int64]*apigen.DeploymentIdentifier, len(rows))
	for _, row := range rows {
		id := &apigen.DeploymentIdentifier{
			Environment: row.Environment,
			Machine:     row.Machine,
			Name:        row.Name,
		}
		idByDBID[row.DeploymentID] = id
	}
	for _, st := range statuses {
		id, ok := idByDBID[st.DeploymentID]
		if !ok {
			continue
		}
		s.statusCache[*id] = statusRowToProto(st.DeploymentID, id, st)
	}

	// Ensure every config has a status entry (invariant: status is never nil).
	now := time.Now().UnixMilli()
	for id, dbID := range s.dbIDCache {
		if _, ok := s.statusCache[id]; ok {
			continue
		}
		idCopy := id
		st := &apigen.DeploymentStatus{
			StatusSeqNo:  0,
			Timestamp:    time.UnixMilli(now),
			DeploymentID: &idCopy,
		}
		if err := s.q.InsertDeploymentStatus(ctx, statusProtoToParams(dbID, st)); err != nil {
			panic(fmt.Sprintf("loadCache: InsertDeploymentStatus (default): %v", err))
		}
		s.statusCache[id] = st
	}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// resolveDeploymentID looks up or creates a deployment_identifiers row,
// returning the internal integer ID.
func (s *StorageAdapter) resolveDeploymentID(ctx context.Context, tx *sql.Tx, id *apigen.DeploymentIdentifier) (int64, error) {
	if dbID, ok := s.dbIDCache[*id]; ok {
		return dbID, nil
	}
	q := s.q.WithTx(tx)
	dbID, err := q.UpsertDeploymentID(ctx, UpsertDeploymentIDParams{
		Environment: id.Environment,
		Machine:     id.Machine,
		Name:        id.Name,
		CreatedAt:   time.Now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	s.dbIDCache[*id] = dbID
	return dbID, nil
}

func (s *StorageAdapter) mustResolveDeploymentID(ctx context.Context, tx *sql.Tx, id *apigen.DeploymentIdentifier) int64 {
	dbID, err := s.resolveDeploymentID(ctx, tx, id)
	if err != nil {
		panic(fmt.Sprintf("resolveDeploymentID: %v", err))
	}
	return dbID
}

// --- PrimaryLocalStore: OperatorStore ---

func (s *StorageAdapter) MustWriteDeploymentStatus(ctx context.Context, id apigen.DeploymentIdentifier, f func(*apigen.DeploymentStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	dbID := s.mustResolveDeploymentID(ctx, tx, &id)

	current := s.statusCache[id]
	if current == nil {
		current = &apigen.DeploymentStatus{DeploymentID: &id}
	}

	f(current)

	if err := q.InsertDeploymentStatus(ctx, statusProtoToParams(dbID, current)); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatus: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.statusCache[id] = current
	s.notifyFromCache(id)
}

func (s *StorageAdapter) MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var snapshot []apigen.DeploymentWithStatus
	for id, cfg := range s.configCache {
		if machine != "" && id.Machine != machine {
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
			return dws.Config.ID.Machine == machine
		}
	}
	sub, _ := s.subs.Subscribe(filter)
	return snapshot, sub.Ch
}

// --- PrimaryLocalStore: deployment history ---

func (s *StorageAdapter) MustFetchDeploymentHistory(id apigen.DeploymentIdentifier) []*apigen.DeploymentConfig {
	ctx := context.Background()
	dbID, err := s.lookupDeploymentID(ctx, &id)
	if err != nil {
		return nil
	}
	rows, err := s.q.ListDeploymentConfigHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentConfigHistory: %v", err))
	}
	out := make([]*apigen.DeploymentConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, configHistoryRowToProto(dbID, &id, r))
	}
	return out
}

func (s *StorageAdapter) MustFetchDeploymentStatusHistory(id apigen.DeploymentIdentifier) []*apigen.DeploymentStatus {
	ctx := context.Background()
	dbID, err := s.lookupDeploymentID(ctx, &id)
	if err != nil {
		return nil
	}
	rows, err := s.q.ListDeploymentStatusHistory(ctx, dbID)
	if err != nil {
		panic(fmt.Sprintf("ListDeploymentStatusHistory: %v", err))
	}
	out := make([]*apigen.DeploymentStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusRowToProto(dbID, &id, r))
	}
	return out
}

// --- PrimaryLocalStore: desired state ---

func (s *StorageAdapter) MustSetDeploymentDesiredState(ctx apigen.Context, id apigen.DeploymentIdentifier, desired apigen.DesiredState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bgCtx := context.Background()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	dbID := s.mustResolveDeploymentID(bgCtx, tx, &id)
	now := time.Now().UnixMilli()

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

	// Read back the updated row to get the new seq_no for history.
	updated, err := q.GetDeploymentConfig(bgCtx, dbID)
	if err != nil {
		panic(fmt.Sprintf("GetDeploymentConfig after update: %v", err))
	}
	if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
		DeploymentID:   dbID,
		SeqNo:          updated.SeqNo,
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

	// Update cache from the row we just read back.
	s.configCache[id] = configDBRowToProto(&id, updated)
	s.notifyFromCache(id)
}

// --- PrimaryLocalStore: user config ---

func (s *StorageAdapter) MustFetchUserConfigVersion() *apigen.UserConfigVersion {
	row, err := s.q.GetLatestUserConfigVersion(context.Background())
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		panic(fmt.Sprintf("GetLatestUserConfigVersion: %v", err))
	}
	return &apigen.UserConfigVersion{
		Version:     int32(row.Version),
		Timestamp:   time.UnixMilli(row.Timestamp),
		UpdatedBy:   int32(row.UpdatedBy),
		YamlContent: row.YamlContent,
	}
}

func (s *StorageAdapter) PutDeploymentUserConfig(ctx apigen.Context, yamlContent string, parseFunc func(string) ([]*apigen.DeploymentConfig, error)) (*apigen.UserConfigVersion, error) {
	parsed, err := parseFunc(yamlContent)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bgCtx := context.Background()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	userID := int64(0)
	if ctx.User != nil {
		userID = int64(ctx.User.ID)
	}

	configRow, err := q.InsertUserConfigVersion(bgCtx, InsertUserConfigVersionParams{
		Timestamp:   now,
		UpdatedBy:   userID,
		YamlContent: yamlContent,
	})
	if err != nil {
		panic(fmt.Sprintf("InsertUserConfigVersion: %v", err))
	}

	// Index parsed deployments by identity key, encoding each spec blob once.
	type wantedDep struct {
		id       *apigen.DeploymentIdentifier
		specBlob []byte
	}
	wantedByKey := make(map[string]wantedDep, len(parsed))
	for _, dep := range parsed {
		key := dep.ID.Environment + ":" + dep.ID.Machine + ":" + dep.ID.Name
		var specBlob []byte
		if dep.Spec != nil {
			specBlob = dep.Spec.Encode()
		}
		wantedByKey[key] = wantedDep{id: dep.ID, specBlob: specBlob}
	}

	// Load all current non-deleted deployments.
	allCurrent, err := q.ListAllDeploymentConfigs(bgCtx)
	if err != nil {
		panic(fmt.Sprintf("ListAllDeploymentConfigs: %v", err))
	}
	currentByKey := make(map[string]ListAllDeploymentConfigsRow, len(allCurrent))
	for _, row := range allCurrent {
		key := row.Environment + ":" + row.Machine + ":" + row.Name
		currentByKey[key] = row
	}

	var changed []apigen.DeploymentIdentifier

	// 1. New or changed deployments from yaml.
	for key, wanted := range wantedByKey {
		existing, exists := currentByKey[key]
		if exists && bytes.Equal(existing.SpecBlob, wanted.specBlob) {
			continue
		}

		dbID, err := s.resolveDeploymentID(bgCtx, tx, wanted.id)
		if err != nil {
			panic(fmt.Sprintf("resolveDeploymentID: %v", err))
		}

		var newSeqNo int64
		var desiredVersion string
		var desiredRunning int64
		if exists {
			newSeqNo = existing.SeqNo + 1
			desiredVersion = existing.DesiredVersion
			desiredRunning = existing.DesiredRunning
		} else {
			newSeqNo = 1
		}

		params := UpsertDeploymentConfigParams{
			DeploymentID:   dbID,
			SeqNo:          newSeqNo,
			UpdatedAt:      now,
			UpdatedBy:      userID,
			SpecBlob:       wanted.specBlob,
			DesiredVersion: desiredVersion,
			DesiredRunning: desiredRunning,
			Deleted:        0,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
			DeploymentID:   dbID,
			SeqNo:          newSeqNo,
			UpdatedAt:      now,
			UpdatedBy:      userID,
			SpecBlob:       wanted.specBlob,
			DesiredVersion: desiredVersion,
			DesiredRunning: desiredRunning,
			Deleted:        0,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory: %v", err))
		}

		s.configCache[*wanted.id] = upsertParamsToProto(wanted.id, params)
		if !exists {
			s.insertDefaultStatus(bgCtx, q, dbID, wanted.id, now)
		}
		changed = append(changed, *wanted.id)
	}

	// 2. Mark deleted: deployments in DB but not in yaml.
	for key, row := range currentByKey {
		if _, wanted := wantedByKey[key]; wanted {
			continue
		}
		newSeqNo := row.SeqNo + 1
		params := UpsertDeploymentConfigParams{
			DeploymentID:   row.DeploymentID,
			SeqNo:          newSeqNo,
			UpdatedAt:      now,
			UpdatedBy:      userID,
			SpecBlob:       row.SpecBlob,
			DesiredVersion: row.DesiredVersion,
			DesiredRunning: row.DesiredRunning,
			Deleted:        1,
		}
		if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
			panic(fmt.Sprintf("UpsertDeploymentConfig (delete): %v", err))
		}
		if err := q.InsertDeploymentConfigHistory(bgCtx, InsertDeploymentConfigHistoryParams{
			DeploymentID:   row.DeploymentID,
			SeqNo:          newSeqNo,
			UpdatedAt:      now,
			UpdatedBy:      userID,
			SpecBlob:       row.SpecBlob,
			DesiredVersion: row.DesiredVersion,
			DesiredRunning: row.DesiredRunning,
			Deleted:        1,
		}); err != nil {
			panic(fmt.Sprintf("InsertDeploymentConfigHistory (delete): %v", err))
		}

		id := apigen.DeploymentIdentifier{
			Environment: row.Environment,
			Machine:     row.Machine,
			Name:        row.Name,
		}
		s.configCache[id] = upsertParamsToProto(&id, params)
		changed = append(changed, id)
	}

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	for _, id := range changed {
		s.notifyFromCache(id)
	}

	return &apigen.UserConfigVersion{
		Version:     int32(configRow.Version),
		Timestamp:   time.UnixMilli(configRow.Timestamp),
		UpdatedBy:   int32(configRow.UpdatedBy),
		YamlContent: configRow.YamlContent,
	}, nil
}

func (s *StorageAdapter) FetchDeploymentUserConfigHistory() []*apigen.UserConfigVersion {
	rows, err := s.q.ListUserConfigVersions(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListUserConfigVersions: %v", err))
	}
	out := make([]*apigen.UserConfigVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, &apigen.UserConfigVersion{
			Version:     int32(r.Version),
			Timestamp:   time.UnixMilli(r.Timestamp),
			UpdatedBy:   int32(r.UpdatedBy),
			YamlContent: r.YamlContent,
		})
	}
	return out
}

// insertDefaultStatus inserts a status_seq_no=0 row for a new deployment and
// populates the statusCache. This ensures the invariant: every deployment in
// configCache always has a corresponding entry in statusCache.
func (s *StorageAdapter) insertDefaultStatus(ctx context.Context, q *Queries, dbID int64, id *apigen.DeploymentIdentifier, now int64) {
	st := &apigen.DeploymentStatus{
		StatusSeqNo:  0,
		Timestamp:    time.UnixMilli(now),
		DeploymentID: id,
	}
	if err := q.InsertDeploymentStatus(ctx, statusProtoToParams(dbID, st)); err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatus (default): %v", err))
	}
	s.statusCache[*id] = st
}

// --- secondary (slave) store: write full config from primary ---

// MustWriteDeploymentConfig persists a full DeploymentConfig received from the
// primary node. It upserts the config row, creates a default status for new
// deployments, and notifies subscribers so the local operator picks it up.
func (s *StorageAdapter) MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bgCtx := context.Background()
	tx, err := s.db.BeginTx(bgCtx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	id := cfg.ID
	dbID := s.mustResolveDeploymentID(bgCtx, tx, id)
	_, exists := s.configCache[*id]

	var specBlob []byte
	if cfg.Spec != nil {
		specBlob = cfg.Spec.Encode()
	}

	desiredVersion := ""
	desiredRunning := int64(0)
	if cfg.DesiredState != nil {
		desiredVersion = cfg.DesiredState.Version
		desiredRunning = boolToInt(cfg.DesiredState.Running)
	}

	params := UpsertDeploymentConfigParams{
		DeploymentID:   dbID,
		SeqNo:          int64(cfg.SeqNo),
		UpdatedAt:      cfg.UpdatedAt.UnixMilli(),
		UpdatedBy:      int64(cfg.UpdatedBy),
		SpecBlob:       specBlob,
		DesiredVersion: desiredVersion,
		DesiredRunning: desiredRunning,
		Deleted:        boolToInt(cfg.Deleted),
	}
	if err := q.UpsertDeploymentConfig(bgCtx, params); err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}

	if !exists {
		now := time.Now().UnixMilli()
		s.insertDefaultStatus(bgCtx, q, dbID, id, now)
	}

	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit: %v", err))
	}

	s.configCache[*id] = cfg
	s.notifyFromCache(*id)
}

// SubscribeDeploymentUpdates returns a channel of deployment changes filtered
// by machine, along with an unsubscribe function the caller must invoke when
// done.
func (s *StorageAdapter) SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func()) {
	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ID.Machine == machine
		}
	}
	sub, unsub := s.subs.Subscribe(filter)
	return sub.Ch, unsub
}

// --- internal helpers ---

func (s *StorageAdapter) lookupDeploymentID(ctx context.Context, id *apigen.DeploymentIdentifier) (int64, error) {
	if dbID, ok := s.dbIDCache[*id]; ok {
		return dbID, nil
	}
	row, err := s.q.GetDeploymentIdentifier(ctx, GetDeploymentIdentifierParams{
		Environment: id.Environment,
		Machine:     id.Machine,
		Name:        id.Name,
	})
	if err != nil {
		return 0, err
	}
	s.dbIDCache[*id] = row.ID
	return row.ID, nil
}

// FetchDeploymentStatus returns the cached status for a deployment, or nil.
func (s *StorageAdapter) FetchDeploymentStatus(id apigen.DeploymentIdentifier) *apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCache[id]
}

func (s *StorageAdapter) notifyFromCache(id apigen.DeploymentIdentifier) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	st := s.statusCache[id]
	slog.Info("store: notifyFromCache",
		"id", fmt.Sprintf("%s:%s:%s", id.Environment, id.Machine, id.Name),
		"configSeqNo", cfg.SeqNo,
		"hasPreparer", st != nil && st.Preparer != nil,
		"hasRunner", st != nil && st.Runner != nil,
	)
	s.subs.Notify(apigen.DeploymentWithStatus{
		Config: cfg,
		Status: st,
	})
}

// --- row <-> proto conversions ---

func statusRowToProto(dbID int64, id *apigen.DeploymentIdentifier, r DeploymentStatusHistory) *apigen.DeploymentStatus {
	st := &apigen.DeploymentStatus{
		StatusSeqNo:  int32(r.StatusSeqNo),
		Timestamp:    time.UnixMilli(r.Timestamp),
		DeploymentID: id,
	}
	if r.PreparerStatus.Valid {
		st.Preparer = &apigen.PreparerStatus{
			DeploymentSeqNo: int32(r.PreparerSeqNo.Int64),
			Artifact:        r.PreparerArtifact.String,
			Status:          apigen.PreparationStatus(r.PreparerStatus.Int64),
		}
	}
	if r.RunnerStatus.Valid {
		st.Runner = &apigen.RunnerStatus{
			DeploymentSeqNo:  int32(r.RunnerSeqNo.Int64),
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

func statusProtoToParams(dbID int64, st *apigen.DeploymentStatus) InsertDeploymentStatusParams {
	p := InsertDeploymentStatusParams{
		DeploymentID: dbID,
		StatusSeqNo:  int64(st.StatusSeqNo),
		Timestamp:    st.Timestamp.UnixMilli(),
	}
	if st.Preparer != nil {
		p.PreparerSeqNo = sql.NullInt64{Int64: int64(st.Preparer.DeploymentSeqNo), Valid: true}
		p.PreparerArtifact = sql.NullString{String: st.Preparer.Artifact, Valid: true}
		p.PreparerStatus = sql.NullInt64{Int64: int64(st.Preparer.Status), Valid: true}
	}
	if st.Runner != nil {
		p.RunnerSeqNo = sql.NullInt64{Int64: int64(st.Runner.DeploymentSeqNo), Valid: true}
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

func configHistoryRowToProto(dbID int64, id *apigen.DeploymentIdentifier, r DeploymentConfigHistory) *apigen.DeploymentConfig {
	spec, _ := apigen.DecodeDeploymentSpec(r.SpecBlob)
	return &apigen.DeploymentConfig{
		ID:        id,
		SeqNo:     int32(r.SeqNo),
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

func configRowToProto(id *apigen.DeploymentIdentifier, r ListAllDeploymentConfigsRow) *apigen.DeploymentConfig {
	spec, _ := apigen.DecodeDeploymentSpec(r.SpecBlob)
	return &apigen.DeploymentConfig{
		ID:        id,
		SeqNo:     int32(r.SeqNo),
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

func configDBRowToProto(id *apigen.DeploymentIdentifier, r DeploymentConfig) *apigen.DeploymentConfig {
	spec, _ := apigen.DecodeDeploymentSpec(r.SpecBlob)
	return &apigen.DeploymentConfig{
		ID:        id,
		SeqNo:     int32(r.SeqNo),
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

func upsertParamsToProto(id *apigen.DeploymentIdentifier, p UpsertDeploymentConfigParams) *apigen.DeploymentConfig {
	spec, _ := apigen.DecodeDeploymentSpec(p.SpecBlob)
	return &apigen.DeploymentConfig{
		ID:        id,
		SeqNo:     int32(p.SeqNo),
		UpdatedAt: time.UnixMilli(p.UpdatedAt),
		UpdatedBy: int32(p.UpdatedBy),
		Spec:      spec,
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
