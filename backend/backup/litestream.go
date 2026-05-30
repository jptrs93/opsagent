package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
)

var activeStore *litestream.Store

func MustRestoreAndStartReplicationIfEnabled() {
	cfg := ainit.Config
	if !configured() {
		return
	}
	if err := validateConfig(); err != nil {
		panic(err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(cfg.DataDir, "primary.db")

	client := s3.NewReplicaClient()
	client.AccessKeyID = cfg.BackupS3AccessKeyID
	client.SecretAccessKey = cfg.BackupS3SecretAccessKey
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

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		slog.Info("local primary database missing; restoring from backup if available", "path", dbPath)
	} else if err != nil {
		panic(fmt.Errorf("stat primary database: %w", err))
	}
	if err := db.EnsureExists(ctx); err != nil {
		panic(fmt.Errorf("restore primary database: %w", err))
	}

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

func configured() bool {
	cfg := ainit.Config
	return cfg.BackupS3AccessKeyID != "" ||
		cfg.BackupS3SecretAccessKey != "" ||
		cfg.BackupS3Bucket != "" ||
		cfg.BackupS3Endpoint != "" ||
		cfg.BackupS3Path != "" && cfg.BackupS3Path != "opsagent/primary"
}

func validateConfig() error {
	cfg := ainit.Config
	if cfg.BackupS3AccessKeyID == "" {
		return fmt.Errorf("OPENDEPLOY_BACKUP_S3_ACCESS_KEY_ID is required when backup is configured")
	}
	if cfg.BackupS3SecretAccessKey == "" {
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
