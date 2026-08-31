package logmigrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	rowGroupRows   = 128 * 1024
	writeBatchRows = 4096
	sortBufferRows = 32 * 1024

	archiveExt      = ".parquet"
	collectorTmpExt = ".parquet.tmp"
	// Distinct from the collector's .parquet.tmp, which the collector's own
	// orphan sweep deletes; ours must survive only until the next Run.
	migrateTmpExt         = ".migrate.tmp"
	migrateSortBufPrefix  = "migrate-sortbuf-"
	metadataSortedKey     = "sorted"
	metadataSortedVal     = "1"
	metadataDeploymentKey = "deployment"
)

// logRow mirrors logmanager's unexported row schema; the two must stay
// identical for as long as this package exists.
type logRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	Seq             int64  `parquet:"seq"`
	Level           string `parquet:"level,optional"`
	Msg             string `parquet:"msg,optional"`
	RawMessage      []byte `parquet:"raw_message"`
}

func sortingColumns() []parquet.SortingColumn {
	return []parquet.SortingColumn{
		parquet.Ascending("time"),
		parquet.Ascending("node"),
		parquet.Ascending("instance_ordinal"),
		parquet.Ascending("run"),
		parquet.Ascending("stream"),
		parquet.Ascending("seq"),
	}
}

func migrateDeploymentArchive(ctx context.Context, dir string, deploymentID int, s *Summary) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.WarnContext(ctx, "listing archive deployment dir failed", "dep", deploymentID, "err", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			s.StrayFiles++
			slog.WarnContext(ctx, "stray file in archive deployment dir: "+e.Name(), "dep", deploymentID)
			continue
		}
		if _, err := time.Parse("20060102", e.Name()); err != nil {
			continue
		}
		migrateDayDir(ctx, filepath.Join(dir, e.Name()), deploymentID, s)
	}
}

func migrateDayDir(ctx context.Context, dir string, deploymentID int, s *Summary) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.WarnContext(ctx, "listing archive day dir failed", "dep", deploymentID, "err", err)
		return
	}
	// Crash residue from a previous interrupted run is swept before anything
	// else so a half-written sibling never shadows a retry.
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, migrateTmpExt) || strings.HasPrefix(name, migrateSortBufPrefix) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				slog.WarnContext(ctx, "removing leftover migration temp file "+name+" failed", "dep", deploymentID, "err", err)
			} else {
				s.TmpFilesSwept++
			}
		}
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
			s.StrayFiles++
			slog.WarnContext(ctx, "stray directory in archive day dir: "+name, "dep", deploymentID)
		case strings.HasSuffix(name, migrateTmpExt) || strings.HasPrefix(name, migrateSortBufPrefix):
			// Swept above.
		case strings.HasSuffix(name, collectorTmpExt):
			// Collector crash orphan; its own startup sweep owns these.
		case strings.HasSuffix(name, archiveExt):
			processArchiveFile(ctx, filepath.Join(dir, name), deploymentID, s)
		default:
			s.StrayFiles++
			slog.WarnContext(ctx, "stray file in archive day dir: "+name, "dep", deploymentID)
		}
	}
}

func processArchiveFile(ctx context.Context, path string, deploymentID int, s *Summary) {
	s.FilesScanned++
	f, err := os.Open(path)
	if err != nil {
		s.FilesFailed++
		slog.WarnContext(ctx, "opening archive file "+filepath.Base(path)+" failed", "dep", deploymentID, "err", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.FilesFailed++
		slog.WarnContext(ctx, "stat of archive file "+filepath.Base(path)+" failed", "dep", deploymentID, "err", err)
		return
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		s.FilesFailed++
		slog.WarnContext(ctx, "reading archive file footer of "+filepath.Base(path)+" failed", "dep", deploymentID, "err", err)
		return
	}
	if _, ok := pf.Schema().Lookup("seq"); ok {
		s.FilesAlreadyModern++
		if v, _ := pf.Lookup(metadataSortedKey); v != metadataSortedVal {
			// Modern schema without the sorted stamp is not written by any
			// code path; readable either way, so record it and leave it be.
			s.Anomalies++
			slog.WarnContext(ctx, "archive file "+filepath.Base(path)+" has seq column but no sorted metadata", "dep", deploymentID)
		}
		return
	}
	deployment, _ := pf.Lookup(metadataDeploymentKey)
	if deployment == "" {
		deployment = strconv.Itoa(deploymentID)
	}
	rows, err := rewriteArchiveFile(path, pf, deployment)
	if err != nil {
		s.FilesFailed++
		slog.WarnContext(ctx, "migrating archive file "+filepath.Base(path)+" failed", "dep", deploymentID, "err", err)
		return
	}
	s.FilesMigrated++
	s.RowsMigrated += rows
	slog.InfoContext(ctx, "migrated archive file "+filepath.Base(path), "dep", deploymentID)
}

// rewriteArchiveFile streams every row of the legacy file (missing columns
// zero-fill, which is the correct legacy value for seq) through a sorting
// writer into a sibling temp file, then atomically renames it over the
// original. The file name — and with it the catalog join key — is preserved.
// A crash before the rename leaves the original intact and the sibling for
// the next run's sweep; a crash after leaves a fully modern file.
func rewriteArchiveFile(path string, pf *parquet.File, deployment string) (int64, error) {
	dir := filepath.Dir(path)
	tmp := path + migrateTmpExt
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (int64, error) {
		_ = out.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	w := parquet.NewSortingWriter[logRow](out,
		sortBufferRows,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.SortingWriterConfig(
			parquet.SortingColumns(sortingColumns()...),
			parquet.SortingBuffers(parquet.NewFileBufferPool(dir, migrateSortBufPrefix+"*")),
		),
	)
	r := parquet.NewGenericReader[logRow](pf)
	defer r.Close()
	batch := make([]logRow, writeBatchRows)
	var rows int64
	for {
		n, err := r.Read(batch)
		if n > 0 {
			if _, werr := w.Write(batch[:n]); werr != nil {
				return fail(werr)
			}
			rows += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fail(err)
		}
	}
	w.SetKeyValueMetadata(metadataDeploymentKey, deployment)
	w.SetKeyValueMetadata(metadataSortedKey, metadataSortedVal)
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return rows, syncDir(dir)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
