package assetstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb"
)

type ReconcileStatus struct {
	TargetS3 bool
	Pending  int
	Error    string
	Running  bool
}

func (s *Store) StartReconciler(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		retryDelay := time.Second
		for {
			err := s.recoverPendingUploads(ctx)
			if err == nil {
				_, err = s.Reconcile(ctx)
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

	for _, meta := range s.DB.ListAllAssetVersions() {
		if ctx.Err() != nil {
			return s.pendingForMode(targetS3), ctx.Err()
		}
		if targetS3 && strings.HasPrefix(meta.Location, "local://") {
			err = s.migrateToS3(ctx, meta.ID, newSettings)
		} else if !targetS3 && strings.HasPrefix(meta.Location, "s3://") {
			err = s.migrateToLocal(ctx, meta.ID, oldSettings)
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

func (s *Store) migrationSettings(migration primarydb.AssetMigration) (*apigen.ClusterSettings, *apigen.ClusterSettings, bool, error) {
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

func (s *Store) migrateToS3(ctx context.Context, assetVersionID int32, target *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, ok := s.DB.GetAssetVersionByID(assetVersionID)
	if !ok || !strings.HasPrefix(asset.Location, "local://") {
		return nil
	}
	client, bucket, err := s.s3Client(target)
	if err != nil {
		return err
	}
	body, err := os.Open(localPath(asset.ID))
	if err != nil {
		return fmt.Errorf("open local large asset %d: %w", asset.ID, err)
	}
	defer body.Close()
	key := objectKey(s.Loader.MustLoadConfigStringValue(target.LargeAssets.S3Path), asset.ID)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(int64(asset.SizeBytes)),
	}); err != nil {
		return fmt.Errorf("migrate large asset %d to s3: %w", asset.ID, err)
	}
	asset = s.DB.UpdateAssetVersionLocation(asset.ID, "s3://"+bucket+"/"+key)
	if err := os.Remove(localPath(asset.ID)); err != nil && !os.IsNotExist(err) {
		slog.Warn("remove migrated local large asset", "asset_id", asset.ID, "err", err)
	}
	s.notifyVersionWritten(asset)
	return nil
}

func (s *Store) recoverPendingUploads(ctx context.Context) error {
	targetS3 := s.backupEnabled()
	for _, meta := range s.DB.ListAllAssetVersionsIncludingPending() {
		if !strings.HasPrefix(meta.Location, "pending://") {
			continue
		}
		if err := s.finishPending(ctx, meta.ID, targetS3); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) finishPending(ctx context.Context, assetVersionID int32, targetS3 bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, ok := s.DB.GetAssetVersionByIDIncludingPending(assetVersionID)
	if !ok || !strings.HasPrefix(asset.Location, "pending://") {
		return nil
	}
	name, err := parsePendingLocation(asset.Location)
	if err != nil {
		return err
	}
	stagedPath := filepath.Join(ainit.StaticConfig.LargeAssetsDir, name)
	sourcePath := stagedPath
	file, err := os.Open(sourcePath)
	if os.IsNotExist(err) {
		sourcePath = localPath(asset.ID)
		file, err = os.Open(sourcePath)
	}
	if os.IsNotExist(err) {
		if targetS3 {
			return s.finishPendingFromS3(ctx, asset)
		}
		s.deleteFailedVersion(asset)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open staged large asset %d: %w", asset.ID, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat staged large asset %d: %w", asset.ID, err)
	}
	if info.Size() != int64(asset.SizeBytes) {
		_ = file.Close()
		if targetS3 {
			if err := s.finishPendingFromS3(ctx, asset); err != nil {
				return err
			}
		} else {
			s.deleteFailedVersion(asset)
		}
		if err := os.Remove(sourcePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove invalid staged large asset", "asset_id", asset.ID, "err", err)
		}
		return nil
	}

	if targetS3 {
		cfg := s.Config()
		client, bucket, err := s.s3Client(cfg)
		if err != nil {
			return err
		}
		key := objectKey(s.Loader.MustLoadConfigStringValue(cfg.LargeAssets.S3Path), asset.ID)
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			Body:          file,
			ContentLength: aws.Int64(int64(asset.SizeBytes)),
		}); err != nil {
			return fmt.Errorf("migrate staged large asset %d to s3: %w", asset.ID, err)
		}
		asset = s.DB.UpdateAssetVersionLocation(asset.ID, "s3://"+bucket+"/"+key)
		if err := os.Remove(sourcePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove migrated staged large asset", "asset_id", asset.ID, "err", err)
		}
	} else {
		if err := file.Close(); err != nil {
			return fmt.Errorf("close staged large asset %d: %w", asset.ID, err)
		}
		if sourcePath != localPath(asset.ID) {
			if err := os.Rename(sourcePath, localPath(asset.ID)); err != nil {
				return fmt.Errorf("store staged large asset %d locally: %w", asset.ID, err)
			}
		}
		if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
			return fmt.Errorf("sync staged large asset directory %d: %w", asset.ID, err)
		}
		asset = s.DB.UpdateAssetVersionLocation(asset.ID, localLocation(asset.ID))
	}
	s.notifyVersionWritten(asset)
	return nil
}

func (s *Store) finishPendingFromS3(ctx context.Context, asset *apigen.AssetVersion) error {
	cfg := s.Config()
	client, bucket, err := s.s3Client(cfg)
	if err != nil {
		return err
	}
	key := objectKey(s.Loader.MustLoadConfigStringValue(cfg.LargeAssets.S3Path), asset.ID)
	result, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		var responseErr interface{ HTTPStatusCode() int }
		if errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound {
			s.deleteFailedVersion(asset)
			return nil
		}
		return fmt.Errorf("check staged large asset %d in s3: %w", asset.ID, err)
	}
	if result.ContentLength == nil || *result.ContentLength != int64(asset.SizeBytes) {
		s.deleteFailedVersion(asset)
		return nil
	}
	asset = s.DB.UpdateAssetVersionLocation(asset.ID, "s3://"+bucket+"/"+key)
	s.notifyVersionWritten(asset)
	return nil
}

func (s *Store) migrateToLocal(ctx context.Context, assetVersionID int32, source *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, ok := s.DB.GetAssetVersionByID(assetVersionID)
	if !ok || !strings.HasPrefix(asset.Location, "s3://") {
		return nil
	}
	if info, err := os.Stat(localPath(asset.ID)); err == nil && info.Size() == int64(asset.SizeBytes) {
		if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
			return fmt.Errorf("sync recovered large asset directory %d: %w", asset.ID, err)
		}
		asset = s.DB.UpdateAssetVersionLocation(asset.ID, localLocation(asset.ID))
		s.notifyVersionWritten(asset)
		return nil
	}
	bucket, key, err := parseS3Location(asset.Location)
	if err != nil {
		return err
	}
	client, _, err := s.s3Client(source)
	if err != nil {
		return err
	}
	res, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download large asset %d from s3: %w", asset.ID, err)
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(ainit.StaticConfig.LargeAssetsDir, ".migration-*")
	if err != nil {
		return fmt.Errorf("stage local large asset %d: %w", asset.ID, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.Copy(tmp, io.LimitReader(res.Body, int64(asset.SizeBytes)+1))
	if copyErr != nil {
		tmp.Close()
		return fmt.Errorf("download large asset %d: %w", asset.ID, copyErr)
	}
	if written != int64(asset.SizeBytes) {
		tmp.Close()
		return fmt.Errorf("downloaded large asset %d has size %d, expected %d", asset.ID, written, asset.SizeBytes)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync local large asset %d: %w", asset.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close local large asset %d: %w", asset.ID, err)
	}
	if err := os.Rename(tmpName, localPath(asset.ID)); err != nil {
		return fmt.Errorf("store migrated large asset %d locally: %w", asset.ID, err)
	}
	if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
		return fmt.Errorf("sync migrated large asset directory %d: %w", asset.ID, err)
	}
	asset = s.DB.UpdateAssetVersionLocation(asset.ID, localLocation(asset.ID))
	s.notifyVersionWritten(asset)
	return nil
}

func (s *Store) backupEnabled() bool {
	return s.Loader.MustLoadConfigBoolValue(s.Config().Backup.Enabled)
}

func (s *Store) pendingForMode(targetS3 bool) int {
	pending := 0
	for _, asset := range s.DB.ListAllAssetVersions() {
		if targetS3 && strings.HasPrefix(asset.Location, "local://") {
			pending++
		}
		if !targetS3 && strings.HasPrefix(asset.Location, "s3://") {
			pending++
		}
	}
	return pending
}

func (s *Store) cleanupInactiveLocalFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := map[string]struct{}{}
	for _, asset := range s.DB.ListAllAssetVersionsIncludingPending() {
		if strings.HasPrefix(asset.Location, "local://") {
			active[fmt.Sprint(asset.ID)] = struct{}{}
		}
		if strings.HasPrefix(asset.Location, "pending://") {
			if name, err := parsePendingLocation(asset.Location); err == nil {
				active[name] = struct{}{}
			}
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
