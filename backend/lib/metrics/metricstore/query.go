package metricstore

import (
	"cmp"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/parquet-go/parquet-go"
)

type Query struct {
	From                time.Time
	To                  time.Time
	DeploymentID        int32
	ScheduledInstanceID int32
}

func (q Query) matches(s *apigen.MetricsSample) bool {
	if q.ScheduledInstanceID != 0 && s.ScheduledInstanceID != q.ScheduledInstanceID {
		return false
	}
	return q.matchesHeader(s.Time, s.DeploymentID)
}

func (q Query) matchesHeader(t int64, deploymentID int32) bool {
	if q.DeploymentID != 0 && deploymentID != q.DeploymentID {
		return false
	}
	if !q.From.IsZero() && t < q.From.UnixMilli() {
		return false
	}
	if !q.To.IsZero() && t >= q.To.UnixMilli() {
		return false
	}
	return true
}

func Scan(ctx context.Context, dir string, nodeID int32, q Query, yield func(*apigen.MetricsSample) bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	firstDay := time.Time{}
	if !q.From.IsZero() {
		firstDay = utcDay(q.From)
	}
	lastDay := time.UnixMilli(math.MaxInt64 / 2)
	if !q.To.IsZero() {
		lastDay = utcDay(q.To.Add(-time.Millisecond))
	}
	seen := map[time.Time]bool{}
	var days []time.Time
	for _, e := range entries {
		var day time.Time
		var ok bool
		if e.IsDir() {
			day, ok = parseDay(e.Name())
		} else {
			day, ok = parseWALName(e.Name())
		}
		if !ok || day.Before(firstDay) || day.After(lastDay) || seen[day] {
			continue
		}
		seen[day] = true
		days = append(days, day)
	}
	slices.SortFunc(days, time.Time.Compare)
	for _, day := range days {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stop, err := scanDay(ctx, dir, nodeID, day, q, yield)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

func Collect(ctx context.Context, dir string, nodeID int32, q Query) ([]*apigen.MetricsSample, error) {
	var out []*apigen.MetricsSample
	err := Scan(ctx, dir, nodeID, q, func(s *apigen.MetricsSample) bool {
		out = append(out, s)
		return true
	})
	if err != nil {
		return nil, err
	}
	return sortDedup(out), nil
}

func scanDay(ctx context.Context, dir string, nodeID int32, day time.Time, q Query, yield func(*apigen.MetricsSample) bool) (bool, error) {
	dd := dayDir(dir, day)
	entries, err := os.ReadDir(dd)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	type sealed struct {
		node int32
		seq  int
		name string
	}
	var files []sealed
	ownSealed := false
	for _, e := range entries {
		n, seq, ok := parseParquetName(e.Name())
		if !ok || strings.HasSuffix(e.Name(), tmpExt) {
			continue
		}
		if n == nodeID {
			ownSealed = true
		}
		files = append(files, sealed{n, seq, e.Name()})
	}
	slices.SortFunc(files, func(a, b sealed) int {
		if c := cmp.Compare(a.node, b.node); c != 0 {
			return c
		}
		return cmp.Compare(a.seq, b.seq)
	})
	for _, f := range files {
		stop, err := scanParquet(ctx, filepath.Join(dd, f.name), q, yield)
		if err != nil || stop {
			return stop, err
		}
	}
	if ownSealed {
		return false, nil
	}
	stop := false
	accept := func(payload []byte) bool {
		t, dep := peekSample(payload)
		return q.matchesHeader(t, dep)
	}
	_, _, err = readWAL(walPath(dir, day), accept, func(s *apigen.MetricsSample) bool {
		if q.matches(s) && !yield(s) {
			stop = true
			return false
		}
		return true
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return stop, nil
}

func scanParquet(ctx context.Context, path string, q Query, yield func(*apigen.MetricsSample) bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		return false, err
	}
	depCol := -1
	if q.DeploymentID != 0 {
		if c, ok := pf.Schema().Lookup("deployment_id"); ok {
			depCol = c.ColumnIndex
		}
	}
	rows := make([]row, batchRows)
	for _, rg := range pf.RowGroups() {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if depCol >= 0 && !groupMayContain(rg, depCol, q.DeploymentID) {
			continue
		}
		stop, err := scanRowGroup(rg, rows, q, yield)
		if err != nil || stop {
			return stop, err
		}
	}
	return false, nil
}

func scanRowGroup(rg parquet.RowGroup, rows []row, q Query, yield func(*apigen.MetricsSample) bool) (bool, error) {
	r := parquet.NewGenericRowGroupReader[row](rg)
	defer r.Close()
	for {
		clear(rows)
		n, err := r.Read(rows)
		for i := range rows[:n] {
			s := sampleFromRow(&rows[i])
			if q.matches(s) && !yield(s) {
				return true, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
}

func groupMayContain(rg parquet.RowGroup, col int, v int32) bool {
	ci, err := rg.ColumnChunks()[col].ColumnIndex()
	if err != nil || ci == nil || ci.NumPages() == 0 {
		return true
	}
	for i := 0; i < ci.NumPages(); i++ {
		if ci.NullPage(i) {
			continue
		}
		if ci.MinValue(i).Int32() <= v && v <= ci.MaxValue(i).Int32() {
			return true
		}
	}
	return false
}
