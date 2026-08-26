package backup

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/timeu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

var (
	activeMu      sync.Mutex
	activeProcess *replicatorProcess
	activeConfig  S3Config
)

type secretStore interface {
	MetaByID(id int32) (secrets.Meta, bool)
	RevealByID(id int32) ([]byte, error)
}

type statusPublisher interface {
	NotifyBackupStatusUpdate(apigen.BackupStatus)
}

type assetStatusSource interface {
	AssetStorageStatus() (targetS3 bool, pending int, running bool, err string)
}

func StartReplication(ctx context.Context, configService *config.Service, secretSource secretStore, publisher statusPublisher, assets assetStatusSource) <-chan struct{} {
	ctx = logu.AddTag(ctx, "Backup")
	done := make(chan struct{})
	filter := newBackupConfigFilter(configService, secretSource)
	sub := configService.SnapshotAndSubscribe(filter.Filter)
	filter.SetInitial(sub.InitialValue)
	go func() {
		defer close(done)
		defer sub.Unsubscribe()
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
				if err := stopReplication(context.WithoutCancel(ctx)); err != nil {
					slog.ErrorContext(ctx, "stop backup replication", "err", err)
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

func runReplication(ctx context.Context, loader config.Loader, cfg *apigen.ClusterSettings, secretSource secretStore, publisher statusPublisher, assets assetStatusSource) {
	defer func() {
		if err := stopReplication(context.WithoutCancel(ctx)); err != nil {
			slog.ErrorContext(ctx, "stop backup replication", "err", err)
		}
		publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: "backup replication is not running"}, assets))
	}()

	backoff := timeu.NewExpBackoff(time.Minute, 5*time.Minute)
	for ctx.Err() == nil {
		backupCfg, err := resolvedBackupConfigFromDynamic(loader, cfg, secretSource)
		if err != nil {
			slog.ErrorContext(ctx, "backup replication config invalid", "err", err)
			publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: err.Error()}, assets))
			backoff.WaitWithContext(ctx)
			continue
		}
		proc, err := startReplication(ctx, backupCfg)
		if err != nil {
			slog.ErrorContext(ctx, "start backup replication", "err", err)
			publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: err.Error()}, assets))
			if stopErr := stopReplication(context.WithoutCancel(ctx)); stopErr != nil {
				slog.ErrorContext(ctx, "stop failed backup replication", "err", stopErr)
			}
			backoff.WaitWithContext(ctx)
			continue
		}
		monitorReplication(ctx, proc, publisher, assets)
		if ctx.Err() != nil {
			continue
		}
		exitMsg := "backup replication process exited"
		if proc.exitErr != nil {
			exitMsg = fmt.Sprintf("backup replication process exited: %v", proc.exitErr)
		}
		slog.ErrorContext(ctx, "backup replication process exited", "err", proc.exitErr)
		publishBackupStatus(publisher, withAssetStatus(apigen.BackupStatus{Configured: true, Error: exitMsg}, assets))
		if stopErr := stopReplication(context.WithoutCancel(ctx)); stopErr != nil {
			slog.ErrorContext(ctx, "stop exited backup replication", "err", stopErr)
		}
		backoff.WaitWithContext(ctx)
	}
}

func startReplication(ctx context.Context, cfg S3Config) (*replicatorProcess, error) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if activeProcess != nil && activeConfig == cfg && !activeProcess.exited() {
		return activeProcess, nil
	}
	if activeProcess != nil {
		if err := closeActiveProcess(ctx); err != nil {
			return nil, err
		}
	}

	dbPath := filepath.Join(ainit.StaticConfig.DataDir, "primary.db")
	proc, err := spawnReplicator(ctx, dbPath, cfg)
	if err != nil {
		return nil, err
	}
	activeProcess = proc
	activeConfig = cfg
	slog.InfoContext(ctx, fmt.Sprintf("started primary database backup replication bucket=%s path=%s", cfg.Bucket, cfg.Path))
	return proc, nil
}

func CurrentStatus(ctx context.Context) apigen.BackupStatus {
	activeMu.Lock()
	defer activeMu.Unlock()

	if activeConfig == (S3Config{}) {
		return apigen.BackupStatus{}
	}
	status := apigen.BackupStatus{Configured: true}
	if activeProcess == nil {
		status.Error = "backup replication is not running"
		return status
	}
	if activeProcess.exited() {
		status.Error = "backup replication process exited"
		if activeProcess.exitErr != nil {
			status.Error = fmt.Sprintf("backup replication process exited: %v", activeProcess.exitErr)
		}
		return status
	}
	reported, seen := activeProcess.lastStatus()
	if !seen {
		return status
	}
	status.Running = reported.Running
	status.LastSuccessfulSyncAt = reported.LastSuccessfulSyncAt
	status.LocalTxid = reported.LocalTxid
	status.RemoteTxid = reported.RemoteTxid
	status.InSync = reported.InSync
	status.Error = reported.Error
	return status
}

func pollBackupStatus(ctx context.Context, publisher statusPublisher, assets assetStatusSource) {
	publishBackupStatusUntil(ctx, nil, publisher, assets)
}

func monitorReplication(ctx context.Context, proc *replicatorProcess, publisher statusPublisher, assets assetStatusSource) {
	publishBackupStatusUntil(ctx, proc.done, publisher, assets)
}

func publishBackupStatusUntil(ctx context.Context, done <-chan struct{}, publisher statusPublisher, assets assetStatusSource) {
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
		case <-done:
			return
		case <-ticker.C:
			publishIfChanged()
		}
	}
}

func withAssetStatus(status apigen.BackupStatus, assets assetStatusSource) apigen.BackupStatus {
	if assets == nil {
		return status
	}
	targetS3, pending, running, assetErr := assets.AssetStorageStatus()
	status.AssetTargetS3 = targetS3
	status.AssetPending = uint32(pending)
	status.AssetMigrationRunning = running
	status.AssetError = assetErr
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
	return closeActiveProcess(ctx)
}

func closeActiveProcess(ctx context.Context) error {
	if activeProcess == nil {
		return nil
	}
	proc := activeProcess
	err := proc.stop()
	activeProcess = nil
	activeConfig = S3Config{}
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "stopped primary database backup replication")
	return nil
}

func configured(loader config.Loader, cfg *apigen.ClusterSettings) bool {
	return loader.MustLoadConfigBoolValue(cfg.Backup.Enabled)
}

func resolvedBackupConfigFromDynamic(loader config.Loader, cfg *apigen.ClusterSettings, secretSource secretStore) (S3Config, error) {
	secretAccessKey, err := revealSecretRef(secretSource, cfg.Backup.S3SecretAccessKey)
	if err != nil {
		return S3Config{}, fmt.Errorf("reveal backup S3 secret access key: %w", err)
	}
	backupCfg := S3Config{
		AccessKeyID:     loader.MustLoadConfigStringValue(cfg.Backup.S3AccessKeyID),
		SecretAccessKey: secretAccessKey,
		Bucket:          loader.MustLoadConfigStringValue(cfg.Backup.S3Bucket),
		Path:            loader.MustLoadConfigStringValue(cfg.Backup.S3Path),
		Region:          loader.MustLoadConfigStringValue(cfg.Backup.S3Region),
		Endpoint:        loader.MustLoadConfigStringValue(cfg.Backup.S3Endpoint),
	}
	if err := validateConfig(backupCfg); err != nil {
		return S3Config{}, err
	}
	return backupCfg, nil
}

func validateConfig(cfg S3Config) error {
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
