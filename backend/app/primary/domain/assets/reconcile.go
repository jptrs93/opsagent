package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// storeRowSweepGrace is how long an unreferenced content-store row survives
// before the sweep reclaims it. It bounds how long an interrupted upload's
// staging row and file linger, and leaves room for future flows that upload
// content before creating the referencing asset.
const storeRowSweepGrace = 24 * time.Hour

const reconcileInterval = time.Hour

type ReconcileStatus struct {
	Target  systemconfig.AssetStorageTarget
	Pending int
	Error   string
	Running bool
}

func (s *Store) StartReconciler(ctx context.Context) <-chan struct{} {
	ctx = logu.AddTag(ctx, "AssetStore")
	done := make(chan struct{})
	startupCutoff := time.Now()
	wake := s.wakeChan()
	go func() {
		defer close(done)
		retryDelay := time.Second
		startupSweepDone := false
		interval := time.NewTimer(reconcileInterval)
		defer interval.Stop()
		for {
			var err error
			if !startupSweepDone {
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
				s.setLastError(err.Error())
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
			if !interval.Stop() {
				select {
				case <-interval.C:
				default:
				}
			}
			interval.Reset(reconcileInterval)
			select {
			case <-ctx.Done():
				return
			case <-s.MigrationWake:
			case <-wake:
			case <-interval.C:
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
	target := s.storageTarget(s.Config())
	migration, running := systemconfig.UnfinishedAssetMigration(s.DB.Queries())
	if running {
		now := time.Now().UnixMilli()
		erru.Must(s.DB.Queries().StartAssetMigration(ctx, pq.StartAssetMigrationParams{StartedAt: now, LastAttemptAt: now, ID: migration.ID}))
	}
	unavailable, err := s.convergeRows(ctx, target)
	pending := s.pendingForTarget(target)
	if err != nil {
		if running {
			s.recordMigrationError(migration.ID, err)
		}
		s.setLastError(err.Error())
		return pending, err
	}
	if unavailable > 0 {
		slog.ErrorContext(ctx, "large asset content rows have no durable copy", "count", unavailable)
		s.setLastError(fmt.Sprintf("%d large asset content row(s) have no durable copy", unavailable))
	} else {
		s.setLastError("")
	}
	if running && pending == 0 {
		erru.Must(s.DB.Queries().FinishAssetMigration(ctx, pq.FinishAssetMigrationParams{FinishedAt: time.Now().UnixMilli(), ID: migration.ID}))
	}
	s.cleanupInactiveLocalFiles(ctx)
	return pending, nil
}

func (s *Store) convergeRows(ctx context.Context, target systemconfig.AssetStorageTarget) (int, error) {
	s.verifyLocalFiles(ctx, target)
	unavailable := 0
	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
		if ctx.Err() != nil {
			return unavailable, ctx.Err()
		}
		if !row.FileBacked() {
			continue
		}
		if row.LocalStatus == 0 && row.RemoteStatus == 0 {
			unavailable++
			continue
		}
		var err error
		switch target {
		case systemconfig.AssetStorageBoth:
			if row.LocalStatus == 0 {
				err = s.ensureLocalCopy(ctx, row.ID)
			} else if row.RemoteStatus == 0 {
				err = s.ensureRemoteCopy(ctx, row.ID)
			}
		case systemconfig.AssetStorageS3:
			if row.RemoteStatus == 0 {
				err = s.ensureRemoteCopy(ctx, row.ID)
			}
			if err == nil && row.LocalStatus == 1 {
				s.dropLocalCopy(ctx, row.ID)
			}
		case systemconfig.AssetStorageLocal:
			if row.RemoteStatus == 1 {
				if row.LocalStatus == 0 {
					err = s.ensureLocalCopy(ctx, row.ID)
				}
				if err == nil {
					s.dropRemoteStatus(row.ID)
				}
			}
		}
		if err != nil {
			return unavailable, err
		}
	}
	return unavailable, nil
}

func (s *Store) verifyLocalFiles(ctx context.Context, target systemconfig.AssetStorageTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(ainit.StaticConfig.LargeAssetsDir)
	if err != nil {
		slog.WarnContext(ctx, "list local large assets for verification", "err", err)
		return
	}
	sizes := make(map[string]int64, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		sizes[entry.Name()] = info.Size()
	}
	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
		if !row.FileBacked() {
			continue
		}
		size, present := sizes[row.ID]
		intact := present && size == row.SizeBytes
		switch {
		case row.LocalStatus == 1 && !intact:
			slog.WarnContext(ctx, fmt.Sprintf("local large asset %s is missing or truncated", row.ID))
			SetAssetStoreLocalStatus(s.DB.Queries(), row.ID, 0)
		case row.LocalStatus == 0 && intact && target.UsesLocal():
			if err := adoptLocalFile(row); err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("adopting local large asset %s failed", row.ID), "err", err)
				continue
			}
			SetAssetStoreLocalStatus(s.DB.Queries(), row.ID, 1)
		}
	}
}

func adoptLocalFile(row AssetStoreMeta) error {
	sum, err := hashFile(localPath(row.ID))
	if err != nil {
		return err
	}
	if sum != row.Sha256 {
		return fmt.Errorf("sha256 %s does not match %s", sum, row.Sha256)
	}
	return syncDir(ainit.StaticConfig.LargeAssetsDir)
}

func (s *Store) ensureRemoteCopy(ctx context.Context, storeID string) error {
	s.mu.Lock()
	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || row.LocalStatus != 1 || row.RemoteStatus == 1 || row.Sha256 == "" {
		s.mu.Unlock()
		return nil
	}
	cfg := s.Config()
	client, bucket, err := s.s3Client(cfg)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	body, err := os.Open(localPath(storeID))
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("local large asset %s is unreadable", storeID), "err", err)
		SetAssetStoreLocalStatus(s.DB.Queries(), storeID, 0)
		s.mu.Unlock()
		return nil
	}
	key := objectKey(s.Loader.MustLoadStringSetting(cfg.LargeAssets.S3Path), storeID)
	s.mu.Unlock()
	defer body.Close()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(row.SizeBytes),
	}); err != nil {
		return fmt.Errorf("copy large asset %s to s3: %w", storeID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || current.Sha256 != row.Sha256 {
		return nil
	}
	SetAssetStoreRemoteStatus(s.DB.Queries(), storeID, 1)
	return nil
}

func (s *Store) ensureLocalCopy(ctx context.Context, storeID string) error {
	s.mu.Lock()
	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || row.RemoteStatus != 1 || row.LocalStatus == 1 || row.Sha256 == "" {
		s.mu.Unlock()
		return nil
	}
	cfg := s.Config()
	s.mu.Unlock()
	tmpName, err := s.downloadToTemp(ctx, row, cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer os.Remove(tmpName)
	current, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || current.Sha256 != row.Sha256 || current.LocalStatus == 1 {
		return nil
	}
	if err := os.Rename(tmpName, localPath(storeID)); err != nil {
		return fmt.Errorf("store large asset %s locally: %w", storeID, err)
	}
	if err := syncDir(ainit.StaticConfig.LargeAssetsDir); err != nil {
		return fmt.Errorf("sync large asset directory: %w", err)
	}
	SetAssetStoreLocalStatus(s.DB.Queries(), storeID, 1)
	return nil
}

func (s *Store) downloadToTemp(ctx context.Context, row pq.AssetStore, cfg *apigen.ClusterSettings) (string, error) {
	client, bucket, err := s.s3Client(cfg)
	if err != nil {
		return "", err
	}
	key := objectKey(s.Loader.MustLoadStringSetting(cfg.LargeAssets.S3Path), row.ID)
	res, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", fmt.Errorf("download large asset %s from s3: %w", row.ID, err)
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(ainit.StaticConfig.LargeAssetsDir, ".migration-*")
	if err != nil {
		return "", fmt.Errorf("stage local large asset %s: %w", row.ID, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(res.Body, row.SizeBytes+1))
	if err != nil {
		return fail(fmt.Errorf("download large asset %s: %w", row.ID, err))
	}
	if written != row.SizeBytes {
		return fail(fmt.Errorf("downloaded large asset %s has size %d, expected %d", row.ID, written, row.SizeBytes))
	}
	if sum := hex.EncodeToString(hasher.Sum(nil)); sum != row.Sha256 {
		return fail(fmt.Errorf("downloaded large asset %s has sha256 %s, expected %s", row.ID, sum, row.Sha256))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync local large asset %s: %w", row.ID, err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("close local large asset %s: %w", row.ID, err)
	}
	return tmpName, nil
}

func (s *Store) dropLocalCopy(ctx context.Context, storeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || row.RemoteStatus != 1 {
		return
	}
	if row.LocalStatus == 1 {
		SetAssetStoreLocalStatus(s.DB.Queries(), storeID, 0)
	}
	if err := os.Remove(localPath(storeID)); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, fmt.Sprintf("removing local large asset %s failed", storeID), "err", err)
	}
}

func (s *Store) dropRemoteStatus(storeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := GetAssetStoreRowByID(s.DB.Queries(), storeID)
	if !ok || row.LocalStatus != 1 {
		return
	}
	SetAssetStoreRemoteStatus(s.DB.Queries(), storeID, 0)
}

func (s *Store) ReconcileStatus() ReconcileStatus {
	target := s.storageTarget(s.Config())
	status := ReconcileStatus{Target: target, Pending: s.pendingForTarget(target), Error: s.LastError()}
	if migration, ok := systemconfig.UnfinishedAssetMigration(s.DB.Queries()); ok {
		status.Running = true
		if status.Error == "" {
			status.Error = migration.LastError
		}
	}
	return status
}

func (s *Store) AssetStorageStatus() (targetS3, keepLocal bool, pending int, running bool, err string) {
	status := s.ReconcileStatus()
	return status.Target.UsesS3(), status.Target == systemconfig.AssetStorageBoth, status.Pending, status.Running, status.Error
}

func (s *Store) LastError() string {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.lastError
}

func (s *Store) setLastError(msg string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.lastError = msg
}

func (s *Store) pendingForTarget(target systemconfig.AssetStorageTarget) int {
	pending := 0
	for _, row := range ListAssetStoreRowMetas(s.DB.Queries()) {
		if !row.FileBacked() || (row.LocalStatus == 0 && row.RemoteStatus == 0) {
			continue
		}
		switch target {
		case systemconfig.AssetStorageBoth:
			if row.LocalStatus != row.RemoteStatus {
				pending++
			}
		case systemconfig.AssetStorageS3:
			if row.LocalStatus == 1 {
				pending++
			}
		case systemconfig.AssetStorageLocal:
			if row.RemoteStatus == 1 {
				pending++
			}
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
