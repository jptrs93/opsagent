package metricstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	DefaultQueryRange = time.Hour
	MaxQueryRange     = 92 * 24 * time.Hour
)

var (
	errInvalidRange   = errors.New("time_end must be after time_start")
	errTooManyBuckets = errors.New("too many buckets for the requested range and step")
)

func sortDedup(samples []*apigen.MetricsSample) []*apigen.MetricsSample {
	slices.SortFunc(samples, compareKeyTime)
	return slices.CompactFunc(samples, func(a, b *apigen.MetricsSample) bool {
		return compareKeyTime(a, b) == 0
	})
}

func ResolveRange(start, end time.Time, now time.Time) (time.Time, time.Time, error) {
	if end.IsZero() {
		end = now
	}
	if start.IsZero() {
		start = end.Add(-DefaultQueryRange)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errInvalidRange
	}
	if end.Sub(start) > MaxQueryRange {
		return time.Time{}, time.Time{}, fmt.Errorf("time range exceeds %s", MaxQueryRange)
	}
	return start, end, nil
}

func RequestFields(names []string) ([]Field, error) {
	if len(names) == 0 {
		return Fields, nil
	}
	out := make([]Field, 0, len(names))
	for _, n := range names {
		f, ok := FieldByName(n)
		if !ok {
			return nil, fmt.Errorf("unknown metric %q", n)
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *Store) QueryResponse(ctx context.Context, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	started := time.Now()
	from, to, err := ResolveRange(req.TimeStart, req.TimeEnd, started)
	if err != nil {
		return nil, err
	}
	fields, err := RequestFields(req.Fields)
	if err != nil {
		return nil, err
	}
	res, err := s.Rollup(ctx, RollupRequest{
		Query: Query{
			From:                from,
			To:                  to,
			DeploymentID:        req.DeploymentID,
			ScheduledInstanceID: req.ScheduledInstanceID,
		},
		Step:        time.Duration(req.StepMs) * time.Millisecond,
		Fields:      fields,
		SpecVersion: req.SpecVersion,
		Run:         req.Run,
	})
	if err != nil {
		return nil, err
	}
	out := &apigen.MetricsQueryResponse{
		TimeStart:   res.Start,
		StepMs:      res.Step.Milliseconds(),
		Buckets:     int32(res.Buckets),
		ScannedRows: int64(res.Scanned),
		TookMs:      int32(time.Since(started).Milliseconds()),
	}
	for _, ser := range res.Series {
		out.Series = append(out.Series, &apigen.MetricsSeries{
			ScheduledInstanceID: ser.Key.ScheduledInstanceID,
			Ordinal:             ser.Key.Ordinal,
			SpecVersion:         ser.Key.SpecVersion,
			Run:                 ser.Key.Run,
			NodeID:              ser.NodeID,
			Field:               ser.Field.Name,
			Kind:                int32(ser.Field.Kind),
			Values:              ser.Values,
		})
	}
	return out, nil
}

func (s *Store) LatestResponse() *apigen.MetricsLatestResponse {
	out := &apigen.MetricsLatestResponse{}
	for _, pair := range s.LatestPairs() {
		out.Entries = append(out.Entries, LatestEntry(pair.Prev, pair.Cur))
	}
	return out
}

func LatestEntry(prev, cur *apigen.MetricsSample) *apigen.MetricsLatestEntry {
	e := &apigen.MetricsLatestEntry{Sample: cur}
	if prev == nil {
		return e
	}
	for _, f := range Fields {
		if f.Kind != Counter {
			continue
		}
		if r, ok := Rate(prev, cur, f); ok {
			e.Rates = append(e.Rates, &apigen.MetricsRate{Field: f.Name, PerSecond: r})
		}
	}
	return e
}
