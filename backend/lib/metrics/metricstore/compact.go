package metricstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	compactGrace     = 10 * time.Minute
	DefaultRetention = 90 * 24 * time.Hour
	rowGroupRows     = 16 * 1024
	sortBufferRows   = 64 * 1024
	batchRows        = 1024
)

func Compact(ctx context.Context, dir string, nodeID int32, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	sealedBefore := utcDay(now.Add(-compactGrace))
	var days []time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if day, ok := parseWALName(e.Name()); ok && day.Before(sealedBefore) {
			days = append(days, day)
		}
	}
	slices.SortFunc(days, time.Time.Compare)
	var firstErr error
	for _, day := range days {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := compactDay(ctx, dir, nodeID, day); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("compacting metrics wal for %s failed", day.Format(dayLayout)), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func compactDay(ctx context.Context, dir string, nodeID int32, day time.Time) error {
	wal := walPath(dir, day)
	dd := dayDir(dir, day)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(dd)
	if err != nil {
		return err
	}
	seq := 1
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), tmpExt) {
			_ = os.Remove(filepath.Join(dd, e.Name()))
			continue
		}
		if n, s, ok := parseParquetName(e.Name()); ok && n == nodeID && s >= seq {
			seq = s + 1
		}
	}
	final := filepath.Join(dd, parquetName(nodeID, seq))
	tmp := final + tmpExt
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	w := parquet.NewSortingWriter[row](f, sortBufferRows,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.SortingWriterConfig(
			parquet.SortingColumns(sortingColumns()...),
			parquet.SortingBuffers(parquet.NewFileBufferPool(dd, "sortbuf-*"+tmpExt)),
		),
	)
	batch := make([]row, 0, batchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := w.Write(batch)
		batch = batch[:0]
		return err
	}
	var writeErr error
	records, clean, err := readWAL(wal, nil, func(s *apigen.MetricsSample) bool {
		batch = append(batch, rowFromSample(s))
		if len(batch) >= batchRows {
			if writeErr = flush(); writeErr != nil {
				return false
			}
		}
		return true
	})
	if err != nil {
		return fail(err)
	}
	if writeErr != nil {
		return fail(writeErr)
	}
	if !clean {
		slog.WarnContext(ctx, fmt.Sprintf("metrics wal %s has a damaged tail; kept %d records", filepath.Base(wal), records))
	}
	if records == 0 {
		_ = f.Close()
		_ = os.Remove(tmp)
		return os.Remove(wal)
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := syncDir(dd); err != nil {
		return err
	}
	return os.Remove(wal)
}

func Retain(dir string, now time.Time, keep time.Duration) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cutoff := utcDay(now.Add(-keep))
	var firstErr error
	for _, e := range entries {
		var day time.Time
		var ok bool
		if e.IsDir() {
			day, ok = parseDay(e.Name())
		} else {
			day, ok = parseWALName(e.Name())
		}
		if !ok || !day.Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
