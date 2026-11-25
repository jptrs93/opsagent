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
	return timeRange{q.From, q.To}.contains(t)
}

func (q Query) spec() scanSpec[apigen.MetricsSample] {
	spec := scanSpec[apigen.MetricsSample]{
		timeRange:    timeRange{q.From, q.To},
		deploymentID: q.DeploymentID,
		acceptPayload: func(payload []byte) bool {
			t, dep := peekSample(payload)
			return q.matchesHeader(t, dep)
		},
		match: q.matches,
	}
	if q.DeploymentID != 0 {
		spec.pruneFile = func(pf *parquet.File) func(parquet.RowGroup) bool {
			c, ok := pf.Schema().Lookup("deployment_id")
			if !ok {
				return nil
			}
			return func(rg parquet.RowGroup) bool { return groupMayContain(rg, c.ColumnIndex, q.DeploymentID) }
		}
	}
	return spec
}

type timeRange struct {
	from time.Time
	to   time.Time
}

func (r timeRange) contains(t int64) bool {
	if !r.from.IsZero() && t < r.from.UnixMilli() {
		return false
	}
	if !r.to.IsZero() && t >= r.to.UnixMilli() {
		return false
	}
	return true
}

type scanSpec[S any] struct {
	timeRange
	deploymentID  int32
	acceptPayload func(payload []byte) bool
	match         func(*S) bool
	pruneFile     func(pf *parquet.File) func(parquet.RowGroup) bool
}

func Scan(ctx context.Context, dir string, nodeID int32, q Query, yield func(*apigen.MetricsSample) bool) error {
	return scan(ctx, runTable, dir, nodeID, q.spec(), yield)
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

func scan[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32, spec scanSpec[S], yield func(*S) bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	firstDay := time.Time{}
	if !spec.from.IsZero() {
		firstDay = utcDay(spec.from)
	}
	lastDay := time.UnixMilli(math.MaxInt64 / 2)
	if !spec.to.IsZero() {
		lastDay = utcDay(spec.to.Add(-time.Millisecond))
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
	matched := func(s *S) bool {
		return spec.contains(t.timeOf(s)) && (spec.match == nil || spec.match(s))
	}
	for _, day := range days {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stop, err := scanDay(ctx, t, dir, nodeID, day, spec, matched, yield)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

func scanDay[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32, day time.Time, spec scanSpec[S], matched func(*S) bool, yield func(*S) bool) (bool, error) {
	dd := dayDir(dir, day)
	entries, err := os.ReadDir(dd)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	type sealed struct {
		sealedName
		name string
	}
	var files []sealed
	renaming := map[int]bool{}
	for _, e := range entries {
		if base, ok := strings.CutSuffix(e.Name(), tmpExt); ok {
			if n, ok := parseParquetName(base); ok && n.nodeID == nodeID {
				renaming[n.seq] = true
			}
			continue
		}
		if n, ok := parseParquetName(e.Name()); ok {
			files = append(files, sealed{n, e.Name()})
		}
	}
	ownSealed := false
	files = slices.DeleteFunc(files, func(f sealed) bool {
		if f.nodeID != nodeID {
			return false
		}
		if renaming[f.seq] {
			return true
		}
		ownSealed = true
		return false
	})
	slices.SortFunc(files, func(a, b sealed) int {
		return cmp.Or(cmp.Compare(a.nodeID, b.nodeID), cmp.Compare(a.seq, b.seq), cmp.Compare(a.deploymentID, b.deploymentID))
	})
	for _, f := range files {
		if f.split && spec.deploymentID != 0 && f.deploymentID != spec.deploymentID {
			continue
		}
		stop, err := scanParquet(ctx, t, filepath.Join(dd, f.name), spec, matched, yield)
		if err != nil || stop {
			return stop, err
		}
	}
	if ownSealed {
		return false, nil
	}
	stop := false
	_, _, err = readWAL(walPath(dir, day), t.decode, spec.acceptPayload, func(s *S) bool {
		if matched(s) && !yield(s) {
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

func scanParquet[S any, R any](ctx context.Context, t *table[S, R], path string, spec scanSpec[S], matched func(*S) bool, yield func(*S) bool) (bool, error) {
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
	var mayContain func(parquet.RowGroup) bool
	if spec.pruneFile != nil {
		mayContain = spec.pruneFile(pf)
	}
	rows := make([]R, batchRows)
	for _, rg := range pf.RowGroups() {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if mayContain != nil && !mayContain(rg) {
			continue
		}
		stop, err := scanRowGroup(t, rg, rows, matched, yield)
		if err != nil || stop {
			return stop, err
		}
	}
	return false, nil
}

func scanRowGroup[S any, R any](t *table[S, R], rg parquet.RowGroup, rows []R, matched func(*S) bool, yield func(*S) bool) (bool, error) {
	r := parquet.NewGenericRowGroupReader[R](rg)
	defer r.Close()
	for {
		clear(rows)
		n, err := r.Read(rows)
		for i := range rows[:n] {
			s := t.fromRow(&rows[i])
			if matched(s) && !yield(s) {
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
