package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logstore"
)

// secondarySchema contains only the tables the secondary node needs.
// These are identical to the primary's tables — no deployment_identifiers,
// config_history, or auth tables.
const secondarySchema = `
CREATE TABLE IF NOT EXISTS deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    seq_no          INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS deployment_status (
    deployment_id           INTEGER PRIMARY KEY,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,
    preparer_seq_no         INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_seq_no           INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER
);
CREATE TABLE IF NOT EXISTS deployment_status_history (
    deployment_id           INTEGER NOT NULL,
    status_seq_no           INTEGER NOT NULL,
    timestamp               INTEGER NOT NULL,
    preparer_seq_no         INTEGER,
    preparer_artifact       TEXT,
    preparer_status         INTEGER,
    runner_seq_no           INTEGER,
    runner_pid              INTEGER,
    runner_artifact         TEXT,
    runner_status           INTEGER,
    runner_num_restarts     INTEGER,
    runner_last_restart_at  INTEGER,
    PRIMARY KEY (deployment_id, status_seq_no)
);
`

// SecondaryStorageAdapter is the storage layer for secondary (slave) nodes.
// It uses the primary's integer ID directly and fully owns deployment status.
type SecondaryStorageAdapter struct {
	db *sql.DB

	mu sync.Mutex

	configCache map[int32]*apigen.DeploymentConfig
	statusCache map[int32]*apigen.DeploymentStatus
	subs        *logstore.Subs[apigen.DeploymentWithStatus]
}

func NewSecondaryStorageAdapter(dbPath string) *SecondaryStorageAdapter {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(secondarySchema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	s := &SecondaryStorageAdapter{
		db:          db,
		configCache: make(map[int32]*apigen.DeploymentConfig),
		statusCache: make(map[int32]*apigen.DeploymentStatus),
		subs:        &logstore.Subs[apigen.DeploymentWithStatus]{},
	}
	s.loadCache()
	return s
}

func (s *SecondaryStorageAdapter) loadCache() {
	ctx := context.Background()

	rows, err := s.db.QueryContext(ctx,
		`SELECT deployment_id, environment, machine, name, seq_no, updated_at, updated_by,
		        spec_blob, desired_version, desired_running, deleted
		 FROM deployment_configs WHERE deleted = 0`)
	if err != nil {
		panic(fmt.Sprintf("loadCache: list configs: %v", err))
	}
	defer rows.Close()

	for rows.Next() {
		var dbID int64
		var env, machine, name string
		var seqNo, updatedAt, updatedBy, desiredRunning, deleted int64
		var specBlob []byte
		var desiredVersion string
		if err := rows.Scan(&dbID, &env, &machine, &name, &seqNo, &updatedAt, &updatedBy,
			&specBlob, &desiredVersion, &desiredRunning, &deleted); err != nil {
			panic(fmt.Sprintf("loadCache: scan config: %v", err))
		}
		id := int32(dbID)
		spec, decErr := apigen.DecodeDeploymentSpec(specBlob)
		if decErr != nil {
			slog.Error("failed decoding deployment spec", "deploymentID", id, "err", decErr)
		}
		s.configCache[id] = &apigen.DeploymentConfig{
			ID: id,
			ConfigID: &apigen.DeploymentIdentifier{
				Environment: env, Machine: machine, Name: name,
			},
			Version:  int32(seqNo),
			UpdatedAt: time.UnixMilli(updatedAt),
			UpdatedBy: int32(updatedBy),
			Spec:      spec,
			DesiredState: &apigen.DesiredState{
				Version: desiredVersion,
				Running: desiredRunning != 0,
			},
			Deleted: deleted != 0,
		}
	}

	statusRows, err := s.db.QueryContext(ctx,
		`SELECT deployment_id, status_seq_no, timestamp,
		        preparer_seq_no, preparer_artifact, preparer_status,
		        runner_seq_no, runner_pid, runner_artifact, runner_status,
		        runner_num_restarts, runner_last_restart_at
		 FROM deployment_status`)
	if err != nil {
		panic(fmt.Sprintf("loadCache: list statuses: %v", err))
	}
	defer statusRows.Close()

	for statusRows.Next() {
		var r DeploymentStatusHistory
		if err := statusRows.Scan(
			&r.DeploymentID, &r.StatusSeqNo, &r.Timestamp,
			&r.PreparerConfigVersion, &r.PreparerArtifact, &r.PreparerStatus,
			&r.RunnerConfigVersion, &r.RunnerPid, &r.RunnerArtifact, &r.RunnerStatus,
			&r.RunnerNumRestarts, &r.RunnerLastRestartAt,
		); err != nil {
			panic(fmt.Sprintf("loadCache: scan status: %v", err))
		}
		id := int32(r.DeploymentID)
		s.statusCache[id] = statusRowToProto(r.DeploymentID, r)
	}

	// Ensure every config has a status entry.
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
		dbID := int64(id)
		params := statusProtoToInsertParams(dbID, st)
		s.execUpsertStatus(ctx, statusInsertToUpsert(params))
		// No history row: the placeholder carries no preparer/runner data,
		// so it would show up as a meaningless "status update" entry in the
		// UI. The current-row upsert above is enough to maintain the
		// status-never-nil invariant.
		s.statusCache[id] = st
	}
}

// MustWriteDeploymentConfig persists a full DeploymentConfig received from the primary.
func (s *SecondaryStorageAdapter) MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := cfg.ID
	dbID := int64(id)
	_, exists := s.configCache[id]

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

	env, machine, name := "", "", ""
	if cfg.ConfigID != nil {
		env = cfg.ConfigID.Environment
		machine = cfg.ConfigID.Machine
		name = cfg.ConfigID.Name
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_configs (deployment_id, environment, machine, name, seq_no, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(deployment_id) DO UPDATE SET
		     environment = excluded.environment, machine = excluded.machine, name = excluded.name,
		     seq_no = excluded.seq_no, updated_at = excluded.updated_at, updated_by = excluded.updated_by,
		     spec_blob = excluded.spec_blob, desired_version = excluded.desired_version,
		     desired_running = excluded.desired_running, deleted = excluded.deleted`,
		dbID, env, machine, name,
		int64(cfg.Version), cfg.UpdatedAt.UnixMilli(), int64(cfg.UpdatedBy),
		specBlob, desiredVersion, desiredRunning, boolToInt(cfg.Deleted))
	if err != nil {
		panic(fmt.Sprintf("UpsertDeploymentConfig: %v", err))
	}

	s.configCache[id] = cfg

	if !exists {
		now := time.Now().UnixMilli()
		st := &apigen.DeploymentStatus{
			StatusSeqNo:  0,
			Timestamp:    time.UnixMilli(now),
			DeploymentID: id,
		}
		params := statusProtoToInsertParams(dbID, st)
		s.execUpsertStatus(ctx, statusInsertToUpsert(params))
		// No history row: the placeholder carries no preparer/runner data,
		// so it would show up as a meaningless "status update" entry in the
		// UI. The current-row upsert above is enough to maintain the
		// status-never-nil invariant.
		s.statusCache[id] = st
	}

	s.notifyFromCache(id)
}

// MustWriteDeploymentStatus applies a mutation to the current status and persists it.
func (s *SecondaryStorageAdapter) MustWriteDeploymentStatus(ctx context.Context, deploymentID int32, f func(*apigen.DeploymentStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.statusCache[deploymentID]
	if current == nil {
		current = &apigen.DeploymentStatus{DeploymentID: deploymentID}
	}

	f(current)

	dbID := int64(deploymentID)
	params := statusProtoToInsertParams(dbID, current)
	s.execUpsertStatus(ctx, statusInsertToUpsert(params))
	s.execInsertStatusHistory(ctx, params)

	s.statusCache[deploymentID] = current
	s.notifyFromCache(deploymentID)
}

// FetchDeploymentStatusHistorySince returns history rows with
// status_seq_no > sinceSeqNo, in ascending order. Used on reconnect to
// replay any history the primary is missing.
func (s *SecondaryStorageAdapter) FetchDeploymentStatusHistorySince(deploymentID int32, sinceSeqNo int32) []*apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	dbID := int64(deploymentID)
	rows, err := s.db.QueryContext(ctx,
		`SELECT deployment_id, status_seq_no, timestamp,
		        preparer_seq_no, preparer_artifact, preparer_status,
		        runner_seq_no, runner_pid, runner_artifact, runner_status,
		        runner_num_restarts, runner_last_restart_at
		 FROM deployment_status_history
		 WHERE deployment_id = ? AND status_seq_no > ?
		 ORDER BY status_seq_no ASC`,
		dbID, int64(sinceSeqNo))
	if err != nil {
		panic(fmt.Sprintf("FetchDeploymentStatusHistorySince: %v", err))
	}
	defer rows.Close()

	var out []*apigen.DeploymentStatus
	for rows.Next() {
		var r DeploymentStatusHistory
		if err := rows.Scan(
			&r.DeploymentID, &r.StatusSeqNo, &r.Timestamp,
			&r.PreparerConfigVersion, &r.PreparerArtifact, &r.PreparerStatus,
			&r.RunnerConfigVersion, &r.RunnerPid, &r.RunnerArtifact, &r.RunnerStatus,
			&r.RunnerNumRestarts, &r.RunnerLastRestartAt,
		); err != nil {
			panic(fmt.Sprintf("FetchDeploymentStatusHistorySince scan: %v", err))
		}
		out = append(out, statusRowToProto(r.DeploymentID, r))
	}
	return out
}

func (s *SecondaryStorageAdapter) MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus) {
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

func (s *SecondaryStorageAdapter) FetchDeploymentStatus(deploymentID int32) *apigen.DeploymentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCache[deploymentID]
}

func (s *SecondaryStorageAdapter) SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func()) {
	var filter func(apigen.DeploymentWithStatus) bool
	if machine != "" {
		filter = func(dws apigen.DeploymentWithStatus) bool {
			return dws.Config.ConfigID != nil && dws.Config.ConfigID.Machine == machine
		}
	}
	sub, unsub := s.subs.Subscribe(filter)
	return sub.Ch, unsub
}

// --- internal helpers ---

func (s *SecondaryStorageAdapter) notifyFromCache(id int32) {
	cfg := s.configCache[id]
	if cfg == nil {
		return
	}
	st := s.statusCache[id]
	s.subs.Notify(apigen.DeploymentWithStatus{Config: cfg, Status: st})
}

func (s *SecondaryStorageAdapter) execUpsertStatus(ctx context.Context, p UpsertDeploymentStatusParams) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_status (deployment_id, status_seq_no, timestamp,
		     preparer_seq_no, preparer_artifact, preparer_status,
		     runner_seq_no, runner_pid, runner_artifact, runner_status,
		     runner_num_restarts, runner_last_restart_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(deployment_id) DO UPDATE SET
		     status_seq_no=excluded.status_seq_no, timestamp=excluded.timestamp,
		     preparer_seq_no=excluded.preparer_seq_no, preparer_artifact=excluded.preparer_artifact,
		     preparer_status=excluded.preparer_status, runner_seq_no=excluded.runner_seq_no,
		     runner_pid=excluded.runner_pid, runner_artifact=excluded.runner_artifact,
		     runner_status=excluded.runner_status, runner_num_restarts=excluded.runner_num_restarts,
		     runner_last_restart_at=excluded.runner_last_restart_at`,
		p.DeploymentID, p.StatusSeqNo, p.Timestamp,
		p.PreparerConfigVersion, p.PreparerArtifact, p.PreparerStatus,
		p.RunnerConfigVersion, p.RunnerPid, p.RunnerArtifact, p.RunnerStatus,
		p.RunnerNumRestarts, p.RunnerLastRestartAt)
	if err != nil {
		panic(fmt.Sprintf("UpsertDeploymentStatus: %v", err))
	}
}

func (s *SecondaryStorageAdapter) execInsertStatusHistory(ctx context.Context, p InsertDeploymentStatusHistoryParams) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_status_history (deployment_id, status_seq_no, timestamp,
		     preparer_seq_no, preparer_artifact, preparer_status,
		     runner_seq_no, runner_pid, runner_artifact, runner_status,
		     runner_num_restarts, runner_last_restart_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.DeploymentID, p.StatusSeqNo, p.Timestamp,
		p.PreparerConfigVersion, p.PreparerArtifact, p.PreparerStatus,
		p.RunnerConfigVersion, p.RunnerPid, p.RunnerArtifact, p.RunnerStatus,
		p.RunnerNumRestarts, p.RunnerLastRestartAt)
	if err != nil {
		panic(fmt.Sprintf("InsertDeploymentStatusHistory: %v", err))
	}
}
