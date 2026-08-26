package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/goutil/envu"
	"github.com/jptrs93/goutil/logu"
)

const (
	childModeReplicate = "replicate"
	childModeRestore   = "restore"
)

type childJob struct {
	Mode       string   `json:"mode"`
	DBPath     string   `json:"db_path"`
	OutputPath string   `json:"output_path,omitempty"`
	S3         S3Config `json:"s3"`
}

type childStatus struct {
	Running              bool      `json:"running"`
	InSync               bool      `json:"in_sync"`
	LocalTxid            uint64    `json:"local_txid"`
	RemoteTxid           uint64    `json:"remote_txid"`
	LastSuccessfulSyncAt time.Time `json:"last_successful_sync_at"`
	Error                string    `json:"error,omitempty"`
}

func RunChildProcess(ctx context.Context) error {
	slog.SetDefault(slog.New(logu.NewJSONLogHandler(os.Stderr, envu.MustGetOrDefault[slog.Level]("LOG_LEVEL", slog.LevelInfo))))
	dec := json.NewDecoder(os.Stdin)
	var job childJob
	if err := dec.Decode(&job); err != nil {
		return fmt.Errorf("decode litestream job from stdin: %w", err)
	}
	switch job.Mode {
	case childModeReplicate:
		return runChildReplicate(ctx, job, io.MultiReader(dec.Buffered(), os.Stdin))
	case childModeRestore:
		return runChildRestore(ctx, job)
	default:
		return fmt.Errorf("unknown litestream job mode %q", job.Mode)
	}
}

func runChildReplicate(ctx context.Context, job childJob, parentPipe io.Reader) (err error) {
	unlock, err := acquireReplicationLock(job.DBPath)
	if err != nil {
		return err
	}
	defer unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, parentPipe)
		cancel()
	}()

	db := litestream.NewDB(job.DBPath)
	db.Replica = litestream.NewReplicaWithClient(db, newReplicaClient(job.S3))
	store := litestream.NewStore([]*litestream.DB{db}, litestream.CompactionLevels{
		{Level: 0},
		{Level: 1, Interval: 10 * time.Second},
	})
	store.ShutdownSyncTimeout = 10 * time.Second
	db.ShutdownSyncTimeout = store.ShutdownSyncTimeout
	if err := store.Open(ctx); err != nil {
		return fmt.Errorf("open backup replication: %w", err)
	}
	defer func() {
		if closeErr := store.Close(context.WithoutCancel(ctx)); closeErr != nil && err == nil {
			err = fmt.Errorf("close backup replication: %w", closeErr)
		}
	}()

	enc := json.NewEncoder(os.Stdout)
	var last childStatus
	var sent bool
	emit := func() {
		status := collectChildStatus(ctx, db)
		if sent && status == last {
			return
		}
		if encodeErr := enc.Encode(status); encodeErr != nil {
			return
		}
		last = status
		sent = true
	}
	emit()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			emit()
		}
	}
}

func collectChildStatus(ctx context.Context, db *litestream.DB) childStatus {
	status := childStatus{Running: true, LastSuccessfulSyncAt: db.LastSuccessfulSyncAt()}
	syncStatus, err := db.SyncStatus(ctx)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.LocalTxid = uint64(syncStatus.LocalTXID)
	status.RemoteTxid = uint64(syncStatus.RemoteTXID)
	status.InSync = syncStatus.InSync
	return status
}

func runChildRestore(ctx context.Context, job childJob) error {
	db := litestream.NewDB(job.DBPath)
	replica := litestream.NewReplicaWithClient(db, newReplicaClient(job.S3))
	restoreOpts := litestream.NewRestoreOptions()
	restoreOpts.OutputPath = job.OutputPath
	return replica.Restore(ctx, restoreOpts)
}

func newReplicaClient(cfg S3Config) *s3.ReplicaClient {
	client := s3.NewReplicaClient()
	client.AccessKeyID = cfg.AccessKeyID
	client.SecretAccessKey = cfg.SecretAccessKey
	client.Bucket = cfg.Bucket
	client.Path = cfg.Path
	client.Region = cfg.Region
	client.Endpoint = cfg.Endpoint
	if cfg.Endpoint != "" {
		client.ForcePathStyle = true
	}
	return client
}

func acquireReplicationLock(dbPath string) (func(), error) {
	lockPath := dbPath + ".litestream-lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open backup replication lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another backup replication process holds %s: %w", lockPath, err)
	}
	return func() { _ = f.Close() }, nil
}
