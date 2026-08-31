// Package logmigrate is the one-time on-disk migration to the seq-column log
// format. It rewrites legacy parquet archive files (no seq column) into the
// modern schema via an atomic sibling-file replace, verifies that live WAL
// files contain only current-format frames, and logs a node status summary.
//
// It runs synchronously at startup on both primary and secondary, strictly
// before logmanager.StartManager, so no collector can race its temp files.
// The row schema and archive constants here deliberately duplicate
// logmanager's unexported ones: the whole package is throwaway and is deleted
// together with the legacy-format read compatibility once every node reports
// clean. Catalog rows in log.db are untouched — file names (the catalog join
// key) are preserved; only the write-only byte_size field goes stale.
package logmigrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jptrs93/goutil/logu"
)

type Summary struct {
	FilesScanned       int
	FilesAlreadyModern int
	FilesMigrated      int
	FilesFailed        int
	RowsMigrated       int64
	TmpFilesSwept      int
	Anomalies          int

	WALFilesScanned  int
	WALFilesLegacy   int
	WALFilesFailed   int
	WALLegacyFrames  int64
	WALFilesTornTail int

	StrayFiles int
}

// Clean reports whether the node holds no legacy-format data and the scan
// completed without errors, i.e. the legacy read paths are safe to remove
// once every node reports clean. Strays and anomalies are informational: they
// are never read through the legacy code paths.
func (s Summary) Clean() bool {
	return s.FilesFailed == 0 && s.WALFilesFailed == 0 && s.WALFilesLegacy == 0
}

// Run migrates every deployment's archive under archiveDir and checks every
// WAL file under walDir. It never fails startup: per-file problems are logged
// and counted in the returned Summary.
func Run(ctx context.Context, walDir, archiveDir string) Summary {
	ctx = logu.AddTag(ctx, "LogMigrate")
	start := time.Now()
	var s Summary
	for _, dep := range deploymentDirs(ctx, archiveDir) {
		migrateDeploymentArchive(ctx, filepath.Join(archiveDir, strconv.Itoa(dep)), dep, &s)
	}
	for _, dep := range deploymentDirs(ctx, walDir) {
		checkWALDir(ctx, filepath.Join(walDir, strconv.Itoa(dep)), dep, &s)
	}
	msg := fmt.Sprintf(
		"log format migration complete in %v: parquet scanned=%d migrated=%d alreadyModern=%d failed=%d rowsMigrated=%d tmpSwept=%d anomalies=%d; wal scanned=%d legacyFiles=%d legacyFrames=%d tornTail=%d failed=%d; stray=%d clean=%v",
		time.Since(start).Round(time.Millisecond),
		s.FilesScanned, s.FilesMigrated, s.FilesAlreadyModern, s.FilesFailed, s.RowsMigrated, s.TmpFilesSwept, s.Anomalies,
		s.WALFilesScanned, s.WALFilesLegacy, s.WALLegacyFrames, s.WALFilesTornTail, s.WALFilesFailed,
		s.StrayFiles, s.Clean(),
	)
	if s.Clean() {
		slog.InfoContext(ctx, msg)
	} else {
		slog.WarnContext(ctx, msg)
	}
	return s
}

// deploymentDirs lists the numeric subdirectories of root (deployment IDs,
// including 0 for the agent system log). Anything else — log.db and friends
// at the archive root, non-numeric entries — is skipped silently.
func deploymentDirs(ctx context.Context, root string) []int {
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.WarnContext(ctx, "listing log root failed", "err", err)
		}
		return nil
	}
	var deps []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		deps = append(deps, id)
	}
	return deps
}
