package backup

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/goutil/timeu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/config"
)

var (
	activeMu     sync.Mutex
	activeStore  *litestream.Store
	activeConfig backupConfig
)

type backupConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
}

func StartReplication(ctx context.Context, configService *config.Service) <-chan struct{} {
	done := make(chan struct{})
	filter := newBackupConfigFilter()
	sub := configService.SnapshotAndSubscribe(filter.Filter)
	filter.SetInitial(sub.InitialValue)
	go func() {
		defer close(done)
		defer sub.UnsubscribeFunc()
		var cancel context.CancelFunc
		var currentDone chan struct{}
		stopCurrent := func() {
			if cancel != nil {
				cancel()
				<-currentDone
				cancel = nil
				currentDone = nil
			}
		}
		apply := func(cfg ainit.DynamicConfiguration) {
			stopCurrent()
			if !configured(cfg) {
				if err := stopReplication(context.Background()); err != nil {
					slog.Error("stop backup replication", "err", err)
				}
				return
			}
			runCtx, c := context.WithCancel(ctx)
			cancel = c
			done := make(chan struct{})
			currentDone = done
			go func() {
				defer close(done)
				runReplication(runCtx, cfg)
			}()
		}

		apply(sub.InitialValue)
		for {
			select {
			case <-ctx.Done():
				stopCurrent()
				return
			case cfg, ok := <-sub.Ch:
				if !ok {
					stopCurrent()
					return
				}
				apply(cfg)
			}
		}
	}()
	return done
}

type backupConfigFilter struct {
	mu   sync.Mutex
	last backupConfigSignal
	seen bool
}

type backupConfigSignal struct {
	Enabled         bool
	AccessKeyID     string
	SecretKey       string
	SecretUpdatedAt time.Time
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
}

func newBackupConfigFilter() *backupConfigFilter {
	return &backupConfigFilter{}
}

func (f *backupConfigFilter) Filter(cfg ainit.DynamicConfiguration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := backupConfigSignalFromDynamic(cfg)
	if f.seen && f.last == next {
		return false
	}
	f.last = next
	f.seen = true
	return true
}

func (f *backupConfigFilter) SetInitial(cfg ainit.DynamicConfiguration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = backupConfigSignalFromDynamic(cfg)
	f.seen = true
}

func backupConfigSignalFromDynamic(cfg ainit.DynamicConfiguration) backupConfigSignal {
	signal := backupConfigSignal{Enabled: cfg.BackupEnabled}
	if !cfg.BackupEnabled {
		return signal
	}
	signal.AccessKeyID = cfg.BackupS3AccessKeyID
	if cfg.BackupS3SecretAccessKey != nil {
		signal.SecretKey = cfg.BackupS3SecretAccessKey.Key()
		signal.SecretUpdatedAt = cfg.BackupS3SecretAccessKey.Updated()
	}
	signal.Bucket = cfg.BackupS3Bucket
	signal.Path = cfg.BackupS3Path
	signal.Region = cfg.BackupS3Region
	signal.Endpoint = cfg.BackupS3Endpoint
	return signal
}

func runReplication(ctx context.Context, cfg ainit.DynamicConfiguration) {
	defer func() {
		if err := stopReplication(context.Background()); err != nil {
			slog.Error("stop backup replication", "err", err)
		}
	}()

	backoff := timeu.NewExpBackoff(time.Minute, 5*time.Minute)
	for ctx.Err() == nil {
		backupCfg, err := backupConfigFromDynamic(cfg)
		if err != nil {
			slog.Error("backup replication config invalid", "err", err)
			backoff.WaitWithContext(ctx)
			continue
		}
		if err := startReplication(ctx, backupCfg); err != nil {
			slog.Error("start backup replication", "err", err)
			if stopErr := stopReplication(context.Background()); stopErr != nil {
				slog.Error("stop failed backup replication", "err", stopErr)
			}
			backoff.WaitWithContext(ctx)
			continue
		}
		<-ctx.Done()
	}
}

func startReplication(ctx context.Context, cfg backupConfig) error {
	activeMu.Lock()
	defer activeMu.Unlock()

	if activeStore != nil && activeConfig == cfg {
		return nil
	}
	if activeStore != nil {
		if err := closeActiveStore(ctx); err != nil {
			return err
		}
	}

	dbPath := filepath.Join(ainit.StaticConfig.DataDir, "primary.db")

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

	db := litestream.NewDB(dbPath)
	replica := litestream.NewReplicaWithClient(db, client)
	db.Replica = replica

	store := litestream.NewStore([]*litestream.DB{db}, litestream.CompactionLevels{
		{Level: 0},
		{Level: 1, Interval: 10 * time.Second},
	})
	if err := store.Open(ctx); err != nil {
		return fmt.Errorf("open backup replication: %w", err)
	}
	activeStore = store
	activeConfig = cfg
	slog.Info("started primary database backup replication", "bucket", cfg.Bucket, "path", cfg.Path)
	return nil
}

func stopReplication(ctx context.Context) error {
	activeMu.Lock()
	defer activeMu.Unlock()
	return closeActiveStore(ctx)
}

func closeActiveStore(ctx context.Context) error {
	if activeStore == nil {
		return nil
	}
	store := activeStore
	activeStore = nil
	activeConfig = backupConfig{}
	if err := store.Close(ctx); err != nil {
		return err
	}
	slog.Info("stopped primary database backup replication")
	return nil
}

func configured(cfg ainit.DynamicConfiguration) bool {
	return cfg.BackupEnabled
}

func backupConfigFromDynamic(cfg ainit.DynamicConfiguration) (backupConfig, error) {
	secretAccessKey, err := revealSecretValue(cfg.BackupS3SecretAccessKey)
	if err != nil {
		return backupConfig{}, fmt.Errorf("reveal backup S3 secret access key: %w", err)
	}
	backupCfg := backupConfig{
		AccessKeyID:     cfg.BackupS3AccessKeyID,
		SecretAccessKey: secretAccessKey,
		Bucket:          cfg.BackupS3Bucket,
		Path:            cfg.BackupS3Path,
		Region:          cfg.BackupS3Region,
		Endpoint:        cfg.BackupS3Endpoint,
	}
	if err := validateConfig(backupCfg); err != nil {
		return backupConfig{}, err
	}
	return backupCfg, nil
}

func validateConfig(cfg backupConfig) error {
	if cfg.AccessKeyID == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_ACCESS_KEY_ID is required when backup is configured")
	}
	if cfg.SecretAccessKey == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_SECRET_ACCESS_KEY is required when backup is configured")
	}
	if cfg.Bucket == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_BUCKET is required when backup is configured")
	}
	if cfg.Path == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_PATH is required when backup is configured")
	}
	if cfg.Region == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_REGION is required when backup is configured")
	}
	return nil
}

func revealSecretValue(value interface {
	Reveal() (string, error)
}) (string, error) {
	if value == nil {
		return "", nil
	}
	return value.Reveal()
}
