package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/s3"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type restoreOptions struct {
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Path            string
	Region          string
	Endpoint        string
	RecoveryCode    string
}

func (o restoreOptions) validate() error {
	fields := []struct {
		flag  string
		value string
	}{
		{"--restore-s3-access-key-id", o.AccessKeyID},
		{"--restore-s3-secret-access-key", o.SecretAccessKey},
		{"--restore-s3-bucket", o.Bucket},
		{"--restore-s3-path", o.Path},
		{"--restore-s3-region", o.Region},
		{"--recovery-code", o.RecoveryCode},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("%s is required when --restore-backup true", field.flag)
		}
		if err := validateInstallStringFlag(field.flag, field.value); err != nil {
			return err
		}
	}
	if o.Endpoint != "" {
		if err := validateInstallStringFlag("--restore-s3-endpoint", o.Endpoint); err != nil {
			return err
		}
	}
	return nil
}

func restorePrimaryBackup(opts restoreOptions, own owner) error {
	dbPath := filepath.Join(dataDir, "primary.db")
	if dryRun {
		planned("restore primary database from s3://%s/%s to %s", opts.Bucket, opts.Path, dbPath)
		planned("unlock restored secrets store and write new local machine key")
		return nil
	}

	if err := ensureNoExistingPrimaryDB(dbPath); err != nil {
		return err
	}

	tmpPath := filepath.Join(dataDir, ".primary.db.restore")
	cleanupRestoreArtifacts(tmpPath)
	defer cleanupRestoreArtifacts(tmpPath)

	client := s3.NewReplicaClient()
	client.AccessKeyID = opts.AccessKeyID
	client.SecretAccessKey = opts.SecretAccessKey
	client.Bucket = opts.Bucket
	client.Path = opts.Path
	client.Region = opts.Region
	client.Endpoint = opts.Endpoint
	if opts.Endpoint != "" {
		client.ForcePathStyle = true
	}

	db := litestream.NewDB(dbPath)
	replica := litestream.NewReplicaWithClient(db, client)
	restoreOpts := litestream.NewRestoreOptions()
	restoreOpts.OutputPath = tmpPath
	if err := replica.Restore(context.Background(), restoreOpts); err != nil {
		return fmt.Errorf("restore primary database: %w", err)
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("install restored primary database: %w", err)
	}
	if err := chmodChown(dbPath, 0o600, own); err != nil {
		return err
	}

	if err := unlockRestoredSecrets(dbPath, opts.RecoveryCode, own); err != nil {
		return fmt.Errorf("%w; delete %s, %s, %s, and %s before trying install recovery again", err, dbPath, dbPath+"-wal", dbPath+"-shm", filepath.Join(dataDir, "machine.key"))
	}
	info("restored primary database and re-established local machine key")
	return nil
}

func ensureNoExistingPrimaryDB(dbPath string) error {
	for _, path := range sqliteArtifactPaths(dbPath) {
		if pathExists(path) {
			return fmt.Errorf("refusing to restore backup because %s already exists; delete %s, %s, %s, and %s before trying install recovery again", path, dbPath, dbPath+"-wal", dbPath+"-shm", filepath.Join(dataDir, "machine.key"))
		}
	}
	return nil
}

func cleanupRestoreArtifacts(dbPath string) {
	for _, path := range sqliteArtifactPaths(dbPath) {
		_ = os.Remove(path)
	}
}

func sqliteArtifactPaths(dbPath string) []string {
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
}

func unlockRestoredSecrets(dbPath, recoveryCode string, own owner) error {
	store := sqlite.NewPrimaryStorage(dbPath)

	mgr, err := secrets.Open(dataDir, store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open restored secrets store: %w", err)
	}
	if err := mgr.Unlock(recoveryCode); err != nil {
		_ = store.Close()
		return fmt.Errorf("unlock restored secrets store: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close restored primary database: %w", err)
	}
	for _, path := range append(sqliteArtifactPaths(dbPath), filepath.Join(dataDir, "machine.key")) {
		if err := chownIfExists(path, own); err != nil {
			return err
		}
	}
	return nil
}

func chmodChown(path string, mode os.FileMode, own owner) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return own.apply(path)
}

func chownIfExists(path string, own owner) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return own.apply(path)
}
