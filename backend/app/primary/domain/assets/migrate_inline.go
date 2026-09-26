package assets

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// MigrateInlineContent is the one-time v0.0.614 move of inline asset blobs out
// of SQLite: every content row whose only copy is the inline blob gets a file
// in the large-asset root and a local claim, then the column is dropped. It is
// idempotent and runs before the reconciler, which then converges the new
// files to the configured target. Remove after every active cluster has
// rolled forward, per the migrations.sql history-note convention.
func (s *Store) MigrateInlineContent(ctx context.Context) error {
	q := s.DB.Queries()
	has, err := q.AssetStoreHasInlineBlobColumn(ctx)
	if err != nil || !has {
		return err
	}
	blobs, err := q.ListInlineAssetBlobs(ctx)
	if err != nil {
		return err
	}
	skipped := 0
	for _, b := range blobs {
		if int64(len(b.Blob)) != b.SizeBytes {
			skipped++
			slog.ErrorContext(ctx, "inline asset content has no copy to write out", "storage_key", b.ID, "sha256", b.Sha256)
			continue
		}
		if err := writeFileAtomic(localPath(b.ID), b.Blob); err != nil {
			return fmt.Errorf("write inline asset %s to the large-asset root: %w", b.ID, err)
		}
		if err := q.MarkInlineAssetBlobExternalized(ctx, b.ID); err != nil {
			return err
		}
	}
	if len(blobs) > 0 {
		slog.InfoContext(ctx, "moved inline asset content to the large-asset root", "count", len(blobs)-skipped)
	}
	if skipped > 0 {
		// Dropping the column here would discard the only bytes those rows have.
		slog.ErrorContext(ctx, "keeping the inline_blob column until every inline asset is written out", "skipped", skipped)
		return nil
	}
	return q.DropAssetStoreInlineBlobColumn(ctx)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".inline-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return syncDir(ainit.StaticConfig.LargeAssetsDir)
}
