package metricstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	return compact(ctx, runTable, dir, nodeID, now)
}

func compact[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32, now time.Time) error {
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
		if err := compactDay(ctx, t, dir, nodeID, day); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("compacting %s wal for %s failed", t.name, day.Format(dayLayout)), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func compactDay[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32, day time.Time) error {
	wal := walPath(dir, day)
	dd := dayDir(dir, day)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		return err
	}
	b, err := os.ReadFile(wal)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	walHash := hex.EncodeToString(sum[:])
	seq, sealed, err := recoverDay(ctx, t, dd, nodeID, walHash)
	if err != nil {
		return err
	}
	if sealed {
		return os.Remove(wal)
	}
	groups := []int32{0}
	clean := true
	if t.split != nil {
		seen := map[int32]bool{}
		groups = groups[:0]
		clean = walkFrames(b, func(payload []byte) bool {
			if d := t.split(payload); !seen[d] {
				seen[d] = true
				groups = append(groups, d)
			}
			return true
		})
		slices.Sort(groups)
	}
	m := manifest{NodeID: nodeID, Seq: seq, WALSha256: walHash}
	discard := func() {
		for _, f := range m.Files {
			_ = os.Remove(filepath.Join(dd, f.Name+tmpExt))
		}
	}
	records := 0
	for _, deploymentID := range groups {
		if ctx.Err() != nil {
			discard()
			return ctx.Err()
		}
		name := parquetName(nodeID, seq)
		var accept func(payload []byte) bool
		if t.split != nil {
			name = splitParquetName(deploymentID, nodeID, seq)
			accept = func(payload []byte) bool { return t.split(payload) == deploymentID }
		}
		f, whole, err := writeSealed(t, dd, name, b, accept)
		if err != nil {
			discard()
			return err
		}
		clean = clean && whole
		if f.Rows == 0 {
			continue
		}
		f.DeploymentID = deploymentID
		m.Files = append(m.Files, f)
		records += int(f.Rows)
	}
	if !clean {
		slog.WarnContext(ctx, fmt.Sprintf("%s wal %s has a damaged tail; kept %d records", t.name, filepath.Base(wal), records))
	}
	if records == 0 {
		return os.Remove(wal)
	}
	if err := writeManifest(dd, m); err != nil {
		_ = os.Remove(filepath.Join(dd, manifestName(nodeID, seq)))
		discard()
		return err
	}
	for _, f := range m.Files {
		if err := os.Rename(filepath.Join(dd, f.Name+tmpExt), filepath.Join(dd, f.Name)); err != nil {
			return err
		}
	}
	if err := syncDir(dd); err != nil {
		return err
	}
	return os.Remove(wal)
}

func recoverDay[S any, R any](ctx context.Context, t *table[S, R], dd string, nodeID int32, walHash string) (seq int, sealed bool, err error) {
	entries, err := os.ReadDir(dd)
	if err != nil {
		return 0, false, err
	}
	seq = 1
	committed := map[string]bool{}
	present := map[string]bool{}
	for _, e := range entries {
		present[strings.TrimSuffix(e.Name(), tmpExt)] = true
	}
	for _, e := range entries {
		n, s, ok := parseManifestName(e.Name())
		if !ok || n != nodeID {
			continue
		}
		seq = max(seq, s+1)
		m, err := readManifest(filepath.Join(dd, e.Name()))
		if err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("%s manifest %s is unreadable; removing it", t.name, e.Name()), "err", err)
			_ = os.Remove(filepath.Join(dd, e.Name()))
			continue
		}
		ofWAL := walHash != "" && m.WALSha256 == walHash
		whole := true
		for _, f := range m.Files {
			whole = whole && present[f.Name]
		}
		// A manifest of this WAL that names a missing file must not count
		// as sealed: the WAL would be removed with the only copy of its rows.
		if ofWAL && !whole {
			slog.WarnContext(ctx, fmt.Sprintf("%s manifest %s names a file that is gone; compacting its wal again", t.name, e.Name()))
			for _, f := range m.Files {
				_ = os.Remove(filepath.Join(dd, f.Name))
				_ = os.Remove(filepath.Join(dd, f.Name+tmpExt))
			}
			_ = os.Remove(filepath.Join(dd, e.Name()))
			continue
		}
		for _, f := range m.Files {
			committed[f.Name] = true
		}
		sealed = sealed || ofWAL
	}
	for _, e := range entries {
		if base, ok := strings.CutSuffix(e.Name(), tmpExt); ok {
			if !committed[base] {
				_ = os.Remove(filepath.Join(dd, e.Name()))
				continue
			}
			if err := os.Rename(filepath.Join(dd, e.Name()), filepath.Join(dd, base)); err != nil {
				return 0, false, err
			}
			continue
		}
		if n, ok := parseParquetName(e.Name()); ok && n.nodeID == nodeID {
			seq = max(seq, n.seq+1)
		}
	}
	return seq, sealed, syncDir(dd)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func writeSealed[S any, R any](t *table[S, R], dd, name string, wal []byte, accept func(payload []byte) bool) (out manifestFile, whole bool, err error) {
	tmp := filepath.Join(dd, name+tmpExt)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return manifestFile{}, false, err
	}
	fail := func(err error) (manifestFile, bool, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return manifestFile{}, false, err
	}
	h := sha256.New()
	cw := &countingWriter{w: io.MultiWriter(f, h)}
	w := parquet.NewSortingWriter[R](cw, sortBufferRows,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.SortingWriterConfig(
			parquet.SortingColumns(t.sorting...),
			parquet.SortingBuffers(parquet.NewFileBufferPool(dd, "sortbuf-*"+tmpExt)),
		),
	)
	batch := make([]R, 0, batchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := w.Write(batch)
		batch = batch[:0]
		return err
	}
	out = manifestFile{Name: name, MinTime: math.MaxInt64, MaxTime: math.MinInt64}
	var writeErr error
	records, whole := walkWAL(wal, t.decode, accept, func(s *S) bool {
		ts := t.timeOf(s)
		out.MinTime, out.MaxTime = min(out.MinTime, ts), max(out.MaxTime, ts)
		batch = append(batch, t.toRow(s))
		if len(batch) >= batchRows {
			if writeErr = flush(); writeErr != nil {
				return false
			}
		}
		return true
	})
	if writeErr != nil {
		return fail(writeErr)
	}
	if records == 0 {
		_ = f.Close()
		_ = os.Remove(tmp)
		return manifestFile{}, whole, nil
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
		return manifestFile{}, false, err
	}
	out.Rows = int64(records)
	out.SizeBytes = cw.n
	out.Sha256 = hex.EncodeToString(h.Sum(nil))
	return out, whole, nil
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
