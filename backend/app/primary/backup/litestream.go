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
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

var (
	activeMu                        sync.Mutex
	activeStore                     *litestream.Store
	activeConfig                    resolvedBackupConfig
	assetMigrationBlocksReplication bool
)

var errAssetMigrationBlocksReplication = fmt.Errorf("asset migration blocks backup replication")

type resolvedBackupConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
}

type secretStore interface {
	MetaByID(id int32) (secrets.Meta, bool)
	RevealByID(id int32) ([]byte, error)
}

type statusPublisher interface {
	NotifyBackupStatusUpdate(apigen.BackupStatus)
}

type assetBackupReadiness interface {
	ReadyForDatabaseBackup() (bool, string)
	AssetStorageStatus() (targetS3 bool, pending int, running bool, err string)
}

func StopReplicationForAssetMigration(ctx context.Context) error {
	activeMu.Lock()
	defer activeMu.Unlock()
	assetMigrationBlocksReplication = true
	return closeActiveStore(ctx)
}

func StartReplication(ctx context.Context, configService *config.Service, secretSource secretStore, publisher statusPublisher, assets assetBackupReadiness) <-chan struct{} {
	done := make(chan struct{})
	filter := newBackupConfigFilter(configService, secretSource)
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
		apply := func(cfg apigen.PrimaryConfig) {
			stopCurrent()
			if !configured(configService, &cfg.Settings) {
				if err := StopReplicationForAssetMigration(context.Background()); err != nil {
					slog.Error("stop backup replication", "err", err)
				}
				runCtx, c := context.WithCancel(ctx)
				cancel = c
				currentDone = make(chan struct{})
				go func(done chan struct{}) {
					defer close(done)
					pollBackupStatus(runCtx, publisher, assets)
				}(currentDone)
				return
			}
			allowReplicationAfterAssetMigration()
			runCtx, c := context.WithCancel(ctx)
			cancel = c
			replicationDone := make(chan struct{})
			currentDone = replicationDone
			go func() {
				defer close(replicationDone)
				runReplication(runCtx, configService, &cfg.Settings, secretSource, publisher, assets)
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
	mu      sync.Mutex
	last    backupConfigSignal
	seen    bool
	loader  config.Loader
	secrets secretStore
}

type backupConfigSignal struct {
	Enabled         bool
	AccessKeyID     string
	SecretID        int32
	SecretUpdatedAt time.Time
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
	ConfigError     string
}

func newBackupConfigFilter(loader config.Loader, secretSource secretStore) *backupConfigFilter {
	return &backupConfigFilter{loader: loader, secrets: secretSource}
}

func (f *backupConfigFilter) Filter(prev, cfg apigen.PrimaryConfig) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := backupConfigSignalFromDynamic(f.loader, &cfg.Settings, f.secrets)
	if f.seen && f.last == next {
		return false
	}
	f.last = next
	f.seen = true
	return true
}

func (f *backupConfigFilter) SetInitial(cfg apigen.PrimaryConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = backupConfigSignalFromDynamic(f.loader, &cfg.Settings, f.secrets)
	f.seen = true
}

func backupConfigSignalFromDynamic(loader config.Loader, cfg *apigen.ClusterSettings, secretSource secretStore) backupConfigSignal {
	enabled := loader.MustLoadConfigBoolValue(cfg.Backup.Enabled)
	signal := backupConfigSignal{Enabled: enabled}
	if !signal.Enabled {
		return signal
	}
	signal.AccessKeyID = loader.MustLoadConfigStringValue(cfg.Backup.S3AccessKeyID)
	secretRef := cfg.Backup.S3SecretAccessKey
	signal.SecretID = secretRef.VersionID
	if secretSource != nil && signal.SecretID != 0 {
		if meta, ok := secretSource.MetaByID(signal.SecretID); ok {
			signal.SecretUpdatedAt = meta.CreatedAt
		}
	}
	signal.Bucket = loader.MustLoadConfigStringValue(cfg.Backup.S3Bucket)
	signal.Path = loader.MustLoadConfigStringValue(cfg.Backup.S3Path)
	signal.Region = loader.MustLoadConfigStringValue(cfg.Backup.S3Region)
	signal.Endpoint = loader.MustLoadConfigStringValue(cfg.Backup.S3Endpoint)
	return signal
}

func runReplication(ctx context.Context, loader config.Loader, cfg *apigen.ClusterSettings, secretSource secretStore, publisher statusPublisher, assets assetBackupReadiness) {
	defer func() {
		if err := stopReplication(context.Background()); err != nil {
			slog.Error("stop backup replication", "err", err)
		}
		publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: "backup replication is not running"}, assets))
	}()

	backoff := timeu.NewExpBackoff(time.Minute, 5*time.Minute)
	for ctx.Err() == nil {
		if assets != nil {
			ready, _ := assets.ReadyForDatabaseBackup()
			if !ready {
				publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true}, assets))
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					continue
				case <-timer.C:
					continue
				}
			}
		}
		backupCfg, err := resolvedBackupConfigFromDynamic(loader, cfg, secretSource)
		if err != nil {
			slog.Error("backup replication config invalid", "err", err)
			publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: err.Error()}, assets))
			backoff.WaitWithContext(ctx)
			continue
		}
		if err := startReplication(ctx, backupCfg); err != nil {
			slog.Error("start backup replication", "err", err)
			publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: err.Error()}, assets))
			if stopErr := stopReplication(context.Background()); stopErr != nil {
				slog.Error("stop failed backup replication", "err", stopErr)
			}
			backoff.WaitWithContext(ctx)
			continue
		}
		pollBackupStatus(ctx, publisher, assets)
	}
}

func startReplication(ctx context.Context, cfg resolvedBackupConfig) error {
	activeMu.Lock()
	defer activeMu.Unlock()
	if assetMigrationBlocksReplication {
		return errAssetMigrationBlocksReplication
	}

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

func allowReplicationAfterAssetMigration() {
	activeMu.Lock()
	assetMigrationBlocksReplication = false
	activeMu.Unlock()
}

func CurrentStatus(ctx context.Context) apigen.BackupStatus {
	activeMu.Lock()
	defer activeMu.Unlock()

	if activeConfig == (resolvedBackupConfig{}) {
		return apigen.BackupStatus{}
	}
	status := apigen.BackupStatus{Configured: true}
	if activeStore == nil {
		status.Error = "backup replication is not running"
		return status
	}
	dbPath := filepath.Join(ainit.StaticConfig.DataDir, "primary.db")
	db := activeStore.FindDB(dbPath)
	if db == nil {
		status.Error = "primary database is not registered with backup replication"
		return status
	}
	status.Running = true
	status.LastSuccessfulSyncAt = db.LastSuccessfulSyncAt()
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

func pollBackupStatus(ctx context.Context, publisher statusPublisher, assets assetBackupReadiness) {
	var last apigen.BackupStatus
	publishIfChanged := func() {
		status := withAssetStatus(CurrentStatus(ctx), assets)
		if status != last {
			publishBackupStatus(publisher, status)
			last = status
		}
	}
	publishIfChanged()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publishIfChanged()
		}
	}
}

func withAssetStatus(status apigen.BackupStatus, assets assetBackupReadiness) apigen.BackupStatus {
	if assets == nil {
		return status
	}
	targetS3, pending, running, assetErr := assets.AssetStorageStatus()
	status.AssetTargetS3 = targetS3
	status.AssetPending = uint32(pending)
	status.AssetMigrationRunning = running
	status.AssetError = assetErr
	if running || pending > 0 {
		status.InSync = false
	}
	return status
}

func publishBackupStatus(publisher statusPublisher, status apigen.BackupStatus) {
	if publisher != nil {
		publisher.NotifyBackupStatusUpdate(status)
	}
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
	if err := store.Close(ctx); err != nil {
		return err
	}
	activeStore = nil
	activeConfig = resolvedBackupConfig{}
	slog.Info("stopped primary database backup replication")
	return nil
}

func configured(loader config.Loader, cfg *apigen.ClusterSettings) bool {
	return loader.MustLoadConfigBoolValue(cfg.Backup.Enabled)
}

func resolvedBackupConfigFromDynamic(loader config.Loader, cfg *apigen.ClusterSettings, secretSource secretStore) (resolvedBackupConfig, error) {
	secretAccessKey, err := revealSecretRef(secretSource, cfg.Backup.S3SecretAccessKey)
	if err != nil {
		return resolvedBackupConfig{}, fmt.Errorf("reveal backup S3 secret access key: %w", err)
	}
	accessKeyID := loader.MustLoadConfigStringValue(cfg.Backup.S3AccessKeyID)
	bucket := loader.MustLoadConfigStringValue(cfg.Backup.S3Bucket)
	path := loader.MustLoadConfigStringValue(cfg.Backup.S3Path)
	region := loader.MustLoadConfigStringValue(cfg.Backup.S3Region)
	endpoint := loader.MustLoadConfigStringValue(cfg.Backup.S3Endpoint)
	backupCfg := resolvedBackupConfig{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		Bucket:          bucket,
		Path:            path,
		Region:          region,
		Endpoint:        endpoint,
	}
	if err := validateConfig(backupCfg); err != nil {
		return resolvedBackupConfig{}, err
	}
	return backupCfg, nil
}

func validateConfig(cfg resolvedBackupConfig) error {
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

func revealSecretRef(secretSource secretStore, ref apigen.SecretRef) (string, error) {
	if secretSource == nil || ref.VersionID == 0 {
		return "", nil
	}
	value, err := secretSource.RevealByID(ref.VersionID)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
