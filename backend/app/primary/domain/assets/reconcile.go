package assets

import (
	"context"
	"fmt"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
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
	ctx = logu.AddTag(ctx, "AssetStore")
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
				err = s.SweepUnreferencedStoreRows(time.Now().Add(-storeRowSweepGrace))
			}
			if err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "reconcile large asset storage", "err", err)
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
	for _, row := range ListUnreferencedAssetStoreRows(s.DB.Queries(), cutoff) {
		if err := os.Remove(localPath(row.ID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove unreferenced large asset %s: %w", row.ID, err)
		}
		DeleteAssetStoreRow(s.DB.Queries(), row.ID)
	}
	return nil
}

func (s *Store) Reconcile(ctx context.Context) (int, error) {
	migration, ok := systemconfig.UnfinishedAssetMigration(s.DB.Queries())
	if !ok {
		s.cleanupInactiveLocalFiles(ctx)
		return 0, nil
	}
	oldSettings, newSettings, targetS3, err := s.migrationSettings(migration)
	if err != nil {
		s.recordMigrationError(migration.ID, err)
		return s.pendingForMode(targetS3), err
	}
	now := time.Now().UnixMilli()
	erru.Must(s.DB.Queries().StartAssetMigration(ctx, pq.StartAssetMigrationParams{StartedAt: now, LastAttemptAt: now, ID: migration.ID}))

	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
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
			s.recordMigrationError(migration.ID, err)
			return s.pendingForMode(targetS3), err
		}
	}

	pending := s.pendingForMode(targetS3)
	if pending == 0 {
		erru.Must(s.DB.Queries().FinishAssetMigration(ctx, pq.FinishAssetMigrationParams{FinishedAt: time.Now().UnixMilli(), ID: migration.ID}))
		s.cleanupInactiveLocalFiles(ctx)
	}
	return pending, nil
}

func (s *Store) migrationSettings(migration pq.AssetMigration) (*apigen.ClusterSettings, *apigen.ClusterSettings, bool, error) {
	oldRow, err := s.DB.Queries().GetConfigByID(context.Background(), migration.OldConfigVersionID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("load old asset migration config %d: %w", migration.OldConfigVersionID, err)
	}
	newRow, err := s.DB.Queries().GetConfigByID(context.Background(), migration.NewConfigVersionID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("load new asset migration config %d: %w", migration.NewConfigVersionID, err)
	}
	oldConfig, err := apigen.DecodeSystemConfig(oldRow.ConfigBlob)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode old asset migration config %d: %w", migration.OldConfigVersionID, err)
	}
	newConfig, err := apigen.DecodeSystemConfig(newRow.ConfigBlob)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode new asset migration config %d: %w", migration.NewConfigVersionID, err)
	}
	targetS3 := s.Loader.MustLoadBoolSetting(newConfig.Settings.Backup.Enabled)
	return &oldConfig.Settings, &newConfig.Settings, targetS3, nil
}

func (s *Store) ReconcileStatus() ReconcileStatus {
	migration, ok := systemconfig.UnfinishedAssetMigration(s.DB.Queries())
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

func (s *Store) AssetStorageStatus() (bool, int, bool, string) {
	status := s.ReconcileStatus()
	return status.TargetS3, status.Pending, status.Running, status.Error
}

func (s *Store) migrateRowToS3(ctx context.Context, storeID string, target *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
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
	key := objectKey(s.Loader.MustLoadStringSetting(target.LargeAssets.S3Path), storeID)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(row.SizeBytes),
	}); err != nil {
		return fmt.Errorf("migrate large asset %s to s3: %w", storeID, err)
	}
	SetAssetStoreRemoteStatus(s.DB.Queries(), storeID, 1)
	if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, fmt.Sprintf("removing migrated local large asset %s failed", storeID), "err", err)
	}
	SetAssetStoreLocalStatus(s.DB.Queries(), storeID, 0)

	return nil
}

func (s *Store) migrateRowToLocal(ctx context.Context, storeID string, source *apigen.ClusterSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
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
		SetAssetStoreLocalStatus(s.DB.Queries(), storeID, 1)
	}
	SetAssetStoreRemoteStatus(s.DB.Queries(), storeID, 0)

	return nil
}

func (s *Store) downloadRowToLocal(ctx context.Context, row pq.AssetStore, source *apigen.ClusterSettings) error {
	client, bucket, err := s.s3Client(source)
	if err != nil {
		return err
	}
	key := objectKey(s.Loader.MustLoadStringSetting(source.LargeAssets.S3Path), row.ID)
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

func (s *Store) backupEnabled() bool {
	return s.Loader.MustLoadBoolSetting(s.Config().Backup.Enabled)
}

func (s *Store) pendingForMode(targetS3 bool) int {
	pending := 0
	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
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

func (s *Store) cleanupInactiveLocalFiles(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := map[string]struct{}{}
	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
		if row.LocalStatus == 1 || row.Staging() {
			active[row.ID] = struct{}{}
		}
	}
	entries, err := os.ReadDir(ainit.StaticConfig.LargeAssetsDir)
	if err != nil {
		slog.WarnContext(ctx, "list local large assets for cleanup", "err", err)
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
			slog.WarnContext(ctx, fmt.Sprintf("removing inactive local large asset %s failed", entry.Name()), "err", err)
		}
	}
}

func (s *Store) recordMigrationError(id int64, migrationErr error) {
	erru.Must(s.DB.Queries().RecordAssetMigrationError(context.Background(), pq.RecordAssetMigrationErrorParams{LastAttemptAt: time.Now().UnixMilli(), LastError: migrationErr.Error(), ID: id}))
}
