package assetstore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// storeRowSweepGrace is how long an unreferenced content-store row survives
// before the sweep reclaims it. It bounds how long an interrupted upload's
// staging row and file linger, and leaves room for future flows that upload
// content before creating the referencing asset.
const storeRowSweepGrace = 24 * time.Hour

type ReconcileStatus struct {
	TargetS3 bool
	Pending  int
	Error    string
	Running  bool
}

func (s *Store) StartReconciler(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	startupCutoff := time.Now()
	go func() {
		defer close(done)
		retryDelay := time.Second
		startupSweepDone := false
		for {
			var err error
			if !startupSweepDone {
				// Nothing references a staging or orphaned row across a
				// restart, so the first sweep reclaims regardless of age.
				err = s.SweepUnreferencedStoreRows(startupCutoff)
				startupSweepDone = err == nil
			}
			if err == nil {
				_, err = s.Reconcile(ctx)
			}
			if err == nil {
				err = s.convertLegacyRows(ctx)
			}
			if err == nil {
				err = s.SweepUnreferencedStoreRows(time.Now().Add(-storeRowSweepGrace))
			}
			if err != nil && ctx.Err() == nil {
				slog.Error("reconcile large asset storage", "err", err)
				timer := time.NewTimer(retryDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if retryDelay < time.Minute {
					retryDelay *= 2
					if retryDelay > time.Minute {
						retryDelay = time.Minute
					}
				}
				continue
			}
			retryDelay = time.Second
			select {
			case <-ctx.Done():
				return
			case <-s.MigrationWake:
			}
		}
	}()
	return done
}

// SweepUnreferencedStoreRows deletes content-store rows no version links to
// that were created before cutoff, along with their local files. S3 objects
// are retained for database restore points.
func (s *Store) SweepUnreferencedStoreRows(cutoff time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.DB.ListUnreferencedAssetStoreRows(cutoff) {
		if err := os.Remove(localPath(row.ID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove unreferenced large asset %s: %w", row.ID, err)
		}
		s.DB.DeleteAssetStoreRow(row.ID)
	}
	return nil
}

func (s *Store) Reconcile(ctx context.Context) (int, error) {
	migration, ok := s.DB.GetUnfinishedAssetMigration()
	if !ok {
		s.cleanupInactiveLocalFiles()
		return 0, nil
	}
	oldSettings, newSettings, targetS3, err := s.migrationSettings(migration)
	if err != nil {
		s.DB.RecordAssetMigrationError(migration.ID, err)
		return s.pendingForMode(targetS3), err
	}
	s.DB.StartAssetMigration(migration.ID)
	if !targetS3 && s.BeforeLocalMigration != nil {
		if err := s.BeforeLocalMigration(ctx); err != nil {
			s.DB.RecordAssetMigrationError(migration.ID, err)
			return s.pendingForMode(targetS3), err
		}
	}

	for _, row := range s.DB.ListAssetStoreRowMetas() {
		if ctx.Err() != nil {
			return s.pendingForMode(targetS3), ctx.Err()
		}
		if !row.FileBacked() {
			continue
		}
		if targetS3 && row.LocalStatus == 1 && row.RemoteStatus == 0 {
			err = s.migrateRowToS3(ctx, row.ID, newSettings)
		} else if !targetS3 && row.RemoteStatus == 1 {
			err = s.migrateRowToLocal(ctx, row.ID, oldSettings)
		}
		if err != nil {
			s.DB.RecordAssetMigrationError(migration.ID, err)
			return s.pendingForMode(targetS3), err
		}
	}

	pending := s.pendingForMode(targetS3)
	if pending == 0 {
		s.DB.FinishAssetMigration(migration.ID)
		s.cleanupInactiveLocalFiles()
	}
	return pending, nil
}

func (s *Store) migrationSettings(migration state.AssetMigration) (*apigen.ClusterSettings, *apigen.ClusterSettings, bool, error) {
	oldRow, err := s.DB.FetchOpenDeployConfigByID(migration.OldConfigVersionID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("load old asset migration config %d: %w", migration.OldConfigVersionID, err)
	}
	newRow, err := s.DB.FetchOpenDeployConfigByID(migration.NewConfigVersionID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("load new asset migration config %d: %w", migration.NewConfigVersionID, err)
	}
	oldConfig, err := apigen.DecodePrimaryConfig(oldRow.ConfigBlob)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode old asset migration config %d: %w", migration.OldConfigVersionID, err)
	}
	newConfig, err := apigen.DecodePrimaryConfig(newRow.ConfigBlob)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode new asset migration config %d: %w", migration.NewConfigVersionID, err)
	}
	targetS3 := s.Loader.MustLoadConfigBoolValue(newConfig.Settings.Backup.Enabled)
	return &oldConfig.Settings, &newConfig.Settings, targetS3, nil
}

func (s *Store) ReconcileStatus() ReconcileStatus {
	migration, ok := s.DB.GetUnfinishedAssetMigration()
	if !ok {
		targetS3 := s.backupEnabled()
		return ReconcileStatus{TargetS3: targetS3, Pending: s.pendingForMode(targetS3)}
	}
	_, _, targetS3, err := s.migrationSettings(migration)
	status := ReconcileStatus{TargetS3: targetS3, Pending: s.pendingForMode(targetS3), Error: migration.LastError, Running: true}
	if err != nil {
		status.Error = err.Error()
	}
	return status
}

func (s *Store) ReadyForDatabaseBackup() (bool, string) {
	status := s.ReconcileStatus()
	if !status.TargetS3 {
		return false, "large asset storage is switching to local mode"
	}
	if status.Running && status.Pending == 0 && status.Error == "" {
		return false, "large asset storage migration is finishing"
	}
	if status.Pending > 0 || status.Error != "" {
		if status.Error != "" {
			return false, status.Error
		}
		return false, fmt.Sprintf("waiting for %d large asset(s) to migrate to S3", status.Pending)
	}
	return true, ""
}

func (s *Store) AssetStorageStatus() (bool, int, bool, string) {
	status := s.ReconcileStatus()
	return status.TargetS3, status.Pending, status.Running, status.Error
}

func (s *Store) migrateRowToS3(ctx context.Context, storeID string, target *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.DB.GetAssetStoreRowByID(storeID)
	if !ok || row.LocalStatus != 1 || row.RemoteStatus == 1 || row.Sha256 == "" {
		return nil
	}
	client, bucket, err := s.s3Client(target)
	if err != nil {
		return err
	}
	body, err := os.Open(localPath(storeID))
	if err != nil {
		return fmt.Errorf("open local large asset %s: %w", storeID, err)
	}
	defer body.Close()
	key := objectKey(s.Loader.MustLoadConfigStringValue(target.LargeAssets.S3Path), storeID)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(row.SizeBytes),
	}); err != nil {
		return fmt.Errorf("migrate large asset %s to s3: %w", storeID, err)
	}
	s.DB.SetAssetStoreRemoteStatus(storeID, 1)
	if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
		slog.Warn("remove migrated local large asset", "store_id", storeID, "err", err)
	}
	s.DB.SetAssetStoreLocalStatus(storeID, 0)
	s.notifyContentMoved(row.Sha256)
	return nil
}

func (s *Store) migrateRowToLocal(ctx context.Context, storeID string, source *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.DB.GetAssetStoreRowByID(storeID)
	if !ok || row.RemoteStatus != 1 || row.Sha256 == "" {
		return nil
	}
	if row.LocalStatus != 1 {
		if info, err := os.Stat(localPath(storeID)); err == nil && info.Size() == row.SizeBytes {
			if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
				return fmt.Errorf("sync recovered large asset directory %s: %w", storeID, err)
			}
		} else {
			if err := s.downloadRowToLocal(ctx, row, source); err != nil {
				return err
			}
		}
		s.DB.SetAssetStoreLocalStatus(storeID, 1)
	}
	s.DB.SetAssetStoreRemoteStatus(storeID, 0)
	s.notifyContentMoved(row.Sha256)
	return nil
}

func (s *Store) downloadRowToLocal(ctx context.Context, row state.AssetStore, source *apigen.ClusterSettings) error {
	client, bucket, err := s.s3Client(source)
	if err != nil {
		return err
	}
	key := objectKey(s.Loader.MustLoadConfigStringValue(source.LargeAssets.S3Path), row.ID)
	res, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download large asset %s from s3: %w", row.ID, err)
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(ainit.StaticConfig.LargeAssetsDir, ".migration-*")
	if err != nil {
		return fmt.Errorf("stage local large asset %s: %w", row.ID, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.Copy(tmp, io.LimitReader(res.Body, row.SizeBytes+1))
	if copyErr != nil {
		tmp.Close()
		return fmt.Errorf("download large asset %s: %w", row.ID, copyErr)
	}
	if written != row.SizeBytes {
		tmp.Close()
		return fmt.Errorf("downloaded large asset %s has size %d, expected %d", row.ID, written, row.SizeBytes)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync local large asset %s: %w", row.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close local large asset %s: %w", row.ID, err)
	}
	if err := os.Rename(tmpName, localPath(row.ID)); err != nil {
		return fmt.Errorf("store migrated large asset %s locally: %w", row.ID, err)
	}
	if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
		return fmt.Errorf("sync migrated large asset directory %s: %w", row.ID, err)
	}
	return nil
}

// convertLegacyRows hashes the content of rows migrated with placeholder shas
// and repoints their version links, merging rows whose content turns out to
// already exist under its real sha.
func (s *Store) convertLegacyRows(ctx context.Context) error {
	for _, row := range s.DB.ListAssetStoreRowMetas() {
		if !row.LegacySha() {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.convertLegacyRow(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) convertLegacyRow(ctx context.Context, storeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.DB.GetAssetStoreRowByID(storeID)
	if !ok || !strings.HasPrefix(row.Sha256, "legacy:") {
		return nil
	}
	var sha string
	switch {
	case row.SizeBytes == 0 || len(row.InlineBlob) > 0:
		sha = hashBlob(row.InlineBlob)
	case row.LocalStatus == 1:
		file, err := os.Open(localPath(storeID))
		if err != nil {
			return fmt.Errorf("open local large asset %s for hashing: %w", storeID, err)
		}
		sha, err = hashReader(file)
		file.Close()
		if err != nil {
			return fmt.Errorf("hash local large asset %s: %w", storeID, err)
		}
	case row.RemoteStatus == 1:
		body, err := s.openS3Asset(ctx, storeID)
		if err != nil {
			return err
		}
		sha, err = hashReader(body)
		body.Close()
		if err != nil {
			return fmt.Errorf("hash s3 large asset %s: %w", storeID, err)
		}
	default:
		slog.Warn("legacy asset store row has no content to hash", "store_id", storeID)
		return nil
	}
	if s.DB.RelinkLegacyAssetSha(storeID, row.Sha256, sha) {
		if row.LocalStatus == 1 {
			if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
				slog.Warn("remove merged duplicate large asset", "store_id", storeID, "err", err)
			}
		}
	}
	s.notifyContentMoved(sha)
	return nil
}

func (s *Store) backupEnabled() bool {
	return s.Loader.MustLoadConfigBoolValue(s.Config().Backup.Enabled)
}

func (s *Store) pendingForMode(targetS3 bool) int {
	pending := 0
	for _, row := range s.DB.ListAssetStoreRowMetas() {
		if !row.FileBacked() {
			continue
		}
		if targetS3 && row.RemoteStatus == 0 {
			pending++
		}
		if !targetS3 && row.RemoteStatus == 1 {
			pending++
		}
	}
	return pending
}

func (s *Store) cleanupInactiveLocalFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := map[string]struct{}{}
	for _, row := range s.DB.ListAssetStoreRowMetas() {
		if row.LocalStatus == 1 || row.Staging() {
			active[row.ID] = struct{}{}
		}
	}
	entries, err := os.ReadDir(ainit.StaticConfig.LargeAssetsDir)
	if err != nil {
		slog.Warn("list local large assets for cleanup", "err", err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := active[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(ainit.StaticConfig.LargeAssetsDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove inactive local large asset", "name", entry.Name(), "err", err)
		}
	}
}
