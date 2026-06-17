package backup

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/config"
)

var activeStore *litestream.Store

func MustRestoreAndStartReplicationIfEnabled(configService *config.Service) {
	sub := configService.SnapshotAndSubscribe()
	cfg := sub.InitialValue
	if !configured(cfg) {
		return
	}
	if err := validateConfig(cfg); err != nil {
		panic(err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(ainit.StaticConfig.DataDir, "primary.db")

	client := s3.NewReplicaClient()
	secretAccessKey, err := revealSecretValue(cfg.BackupS3SecretAccessKey)
	if err != nil {
		panic(fmt.Errorf("reveal backup S3 secret access key: %w", err))
	}
	client.AccessKeyID = cfg.BackupS3AccessKeyID
	client.SecretAccessKey = secretAccessKey
	client.Bucket = cfg.BackupS3Bucket
	client.Path = cfg.BackupS3Path
	client.Region = cfg.BackupS3Region
	client.Endpoint = cfg.BackupS3Endpoint
	if cfg.BackupS3Endpoint != "" {
		client.ForcePathStyle = true
	}

	db := litestream.NewDB(dbPath)
	replica := litestream.NewReplicaWithClient(db, client)
	db.Replica = replica

	// Restore is disabled while backup configuration moves into system_config.
	// if err := db.EnsureExists(ctx); err != nil {
	// 	panic(fmt.Errorf("restore primary database: %w", err))
	// }

	store := litestream.NewStore([]*litestream.DB{db}, litestream.CompactionLevels{
		{Level: 0},
		{Level: 1, Interval: 10 * time.Second},
	})
	if err := store.Open(ctx); err != nil {
		panic(fmt.Errorf("open backup replication: %w", err))
	}
	activeStore = store
	slog.Info("started primary database backup replication", "bucket", cfg.BackupS3Bucket, "path", cfg.BackupS3Path)
}

func configured(cfg ainit.DynamicConfiguration) bool {
	return cfg.BackupEnabled
}

func validateConfig(cfg ainit.DynamicConfiguration) error {
	if cfg.BackupS3AccessKeyID == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_ACCESS_KEY_ID is required when backup is configured")
	}
	secretAccessKey, err := revealSecretValue(cfg.BackupS3SecretAccessKey)
	if err != nil {
		return fmt.Errorf("reveal backup S3 secret access key: %w", err)
	}
	if secretAccessKey == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_SECRET_ACCESS_KEY is required when backup is configured")
	}
	if cfg.BackupS3Bucket == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_BUCKET is required when backup is configured")
	}
	if cfg.BackupS3Path == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_PATH is required when backup is configured")
	}
	if cfg.BackupS3Region == "" {
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
