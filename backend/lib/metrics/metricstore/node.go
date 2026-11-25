package metricstore

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/parquet-go/parquet-go"
)

var DefaultNode *NodeStore

func NodeDir(metricsDir string) string {
	return filepath.Join(metricsDir, "node")
}

type NodeField = FieldOf[apigen.NodeMetricsSample]

type nodeSample = apigen.NodeMetricsSample

var NodeFields = []NodeField{
	{Name: "cpu_usage_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuUsageUsec }},
	{Name: "cpu_user_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuUserUsec }},
	{Name: "cpu_system_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuSystemUsec }},
	{Name: "cpu_iowait_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuIowaitUsec }},
	{Name: "cpu_steal_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuStealUsec }},
	{Name: "cpu_count", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.CpuCount }},
	{Name: "load1", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.Load1 }},
	{Name: "load5", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.Load5 }},
	{Name: "load15", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.Load15 }},
	{Name: "mem_total", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.MemTotal }},
	{Name: "mem_available", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.MemAvailable }},
	{Name: "mem_used", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return difference(s.MemTotal, s.MemAvailable) }},
	{Name: "mem_free", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.MemFree }},
	{Name: "mem_cached", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.MemCached }},
	{Name: "swap_total", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.SwapTotal }},
	{Name: "swap_used", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return difference(s.SwapTotal, s.SwapFree) }},
	{Name: "swap_free", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.SwapFree }},
	{Name: "psi_cpu_some_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuSomeAvg10 }},
	{Name: "psi_cpu_some_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuSomeAvg60 }},
	{Name: "psi_cpu_some_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuSomeAvg300 }},
	{Name: "psi_cpu_some_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiCpuSomeTotalUsec }},
	{Name: "psi_cpu_full_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuFullAvg10 }},
	{Name: "psi_cpu_full_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuFullAvg60 }},
	{Name: "psi_cpu_full_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiCpuFullAvg300 }},
	{Name: "psi_cpu_full_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiCpuFullTotalUsec }},
	{Name: "psi_mem_some_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemSomeAvg10 }},
	{Name: "psi_mem_some_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemSomeAvg60 }},
	{Name: "psi_mem_some_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemSomeAvg300 }},
	{Name: "psi_mem_some_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiMemSomeTotalUsec }},
	{Name: "psi_mem_full_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemFullAvg10 }},
	{Name: "psi_mem_full_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemFullAvg60 }},
	{Name: "psi_mem_full_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiMemFullAvg300 }},
	{Name: "psi_mem_full_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiMemFullTotalUsec }},
	{Name: "psi_io_some_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoSomeAvg10 }},
	{Name: "psi_io_some_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoSomeAvg60 }},
	{Name: "psi_io_some_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoSomeAvg300 }},
	{Name: "psi_io_some_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiIoSomeTotalUsec }},
	{Name: "psi_io_full_avg10", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoFullAvg10 }},
	{Name: "psi_io_full_avg60", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoFullAvg60 }},
	{Name: "psi_io_full_avg300", Kind: Average, Float: func(s *nodeSample) apigen.Maybe[float64] { return s.PsiIoFullAvg300 }},
	{Name: "psi_io_full_total_usec", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.PsiIoFullTotalUsec }},
	{Name: "net_rx_bytes", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetRxBytes }},
	{Name: "net_rx_packets", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetRxPackets }},
	{Name: "net_rx_dropped", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetRxDropped }},
	{Name: "net_tx_bytes", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetTxBytes }},
	{Name: "net_tx_packets", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetTxPackets }},
	{Name: "net_tx_dropped", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.NetTxDropped }},
	{Name: "io_read_bytes", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.IoReadBytes }},
	{Name: "io_write_bytes", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.IoWriteBytes }},
	{Name: "io_read_ops", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.IoReadOps }},
	{Name: "io_write_ops", Kind: Counter, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.IoWriteOps }},
	{Name: "fs_data_total_bytes", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.FsDataTotalBytes }},
	{Name: "fs_data_used_bytes", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return difference(s.FsDataTotalBytes, s.FsDataFreeBytes) }},
	{Name: "fs_data_free_bytes", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.FsDataFreeBytes }},
	{Name: "fs_data_avail_bytes", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.FsDataAvailBytes }},
	{Name: "fs_data_inodes_total", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.FsDataInodesTotal }},
	{Name: "fs_data_inodes_used", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return difference(s.FsDataInodesTotal, s.FsDataInodesFree) }},
	{Name: "fs_data_inodes_free", Kind: Gauge, Int: func(s *nodeSample) apigen.Maybe[int64] { return s.FsDataInodesFree }},
}

func difference(total, rest apigen.Maybe[int64]) apigen.Maybe[int64] {
	if !total.Present || !rest.Present {
		return apigen.Maybe[int64]{}
	}
	return apigen.Some(total.Value - rest.Value)
}

func NodeRequestFields(names []string) ([]NodeField, error) {
	return requestFields(NodeFields, names)
}

type fsRow struct {
	TotalBytes  int64 `parquet:"total_bytes"`
	FreeBytes   int64 `parquet:"free_bytes"`
	AvailBytes  int64 `parquet:"avail_bytes"`
	InodesTotal int64 `parquet:"inodes_total"`
	InodesFree  int64 `parquet:"inodes_free"`
}

type nodeRow struct {
	Time                int64            `parquet:"time,timestamp(millisecond)"`
	NodeID              int32            `parquet:"node_id"`
	CpuUsageUsec        *int64           `parquet:"cpu_usage_usec"`
	CpuUserUsec         *int64           `parquet:"cpu_user_usec"`
	CpuSystemUsec       *int64           `parquet:"cpu_system_usec"`
	CpuIowaitUsec       *int64           `parquet:"cpu_iowait_usec"`
	CpuStealUsec        *int64           `parquet:"cpu_steal_usec"`
	CpuCount            *int64           `parquet:"cpu_count"`
	Load1               *float64         `parquet:"load1"`
	Load5               *float64         `parquet:"load5"`
	Load15              *float64         `parquet:"load15"`
	MemTotal            *int64           `parquet:"mem_total"`
	MemAvailable        *int64           `parquet:"mem_available"`
	MemFree             *int64           `parquet:"mem_free"`
	MemCached           *int64           `parquet:"mem_cached"`
	SwapTotal           *int64           `parquet:"swap_total"`
	SwapFree            *int64           `parquet:"swap_free"`
	PsiCpuSomeAvg10     *float64         `parquet:"psi_cpu_some_avg10"`
	PsiCpuSomeAvg60     *float64         `parquet:"psi_cpu_some_avg60"`
	PsiCpuSomeAvg300    *float64         `parquet:"psi_cpu_some_avg300"`
	PsiCpuSomeTotalUsec *int64           `parquet:"psi_cpu_some_total_usec"`
	PsiCpuFullAvg10     *float64         `parquet:"psi_cpu_full_avg10"`
	PsiCpuFullAvg60     *float64         `parquet:"psi_cpu_full_avg60"`
	PsiCpuFullAvg300    *float64         `parquet:"psi_cpu_full_avg300"`
	PsiCpuFullTotalUsec *int64           `parquet:"psi_cpu_full_total_usec"`
	PsiMemSomeAvg10     *float64         `parquet:"psi_mem_some_avg10"`
	PsiMemSomeAvg60     *float64         `parquet:"psi_mem_some_avg60"`
	PsiMemSomeAvg300    *float64         `parquet:"psi_mem_some_avg300"`
	PsiMemSomeTotalUsec *int64           `parquet:"psi_mem_some_total_usec"`
	PsiMemFullAvg10     *float64         `parquet:"psi_mem_full_avg10"`
	PsiMemFullAvg60     *float64         `parquet:"psi_mem_full_avg60"`
	PsiMemFullAvg300    *float64         `parquet:"psi_mem_full_avg300"`
	PsiMemFullTotalUsec *int64           `parquet:"psi_mem_full_total_usec"`
	PsiIoSomeAvg10      *float64         `parquet:"psi_io_some_avg10"`
	PsiIoSomeAvg60      *float64         `parquet:"psi_io_some_avg60"`
	PsiIoSomeAvg300     *float64         `parquet:"psi_io_some_avg300"`
	PsiIoSomeTotalUsec  *int64           `parquet:"psi_io_some_total_usec"`
	PsiIoFullAvg10      *float64         `parquet:"psi_io_full_avg10"`
	PsiIoFullAvg60      *float64         `parquet:"psi_io_full_avg60"`
	PsiIoFullAvg300     *float64         `parquet:"psi_io_full_avg300"`
	PsiIoFullTotalUsec  *int64           `parquet:"psi_io_full_total_usec"`
	NetRxBytes          *int64           `parquet:"net_rx_bytes"`
	NetRxPackets        *int64           `parquet:"net_rx_packets"`
	NetRxDropped        *int64           `parquet:"net_rx_dropped"`
	NetTxBytes          *int64           `parquet:"net_tx_bytes"`
	NetTxPackets        *int64           `parquet:"net_tx_packets"`
	NetTxDropped        *int64           `parquet:"net_tx_dropped"`
	IoReadBytes         *int64           `parquet:"io_read_bytes"`
	IoWriteBytes        *int64           `parquet:"io_write_bytes"`
	IoReadOps           *int64           `parquet:"io_read_ops"`
	IoWriteOps          *int64           `parquet:"io_write_ops"`
	FsDataTotalBytes    *int64           `parquet:"fs_data_total_bytes"`
	FsDataFreeBytes     *int64           `parquet:"fs_data_free_bytes"`
	FsDataAvailBytes    *int64           `parquet:"fs_data_avail_bytes"`
	FsDataInodesTotal   *int64           `parquet:"fs_data_inodes_total"`
	FsDataInodesFree    *int64           `parquet:"fs_data_inodes_free"`
	ExtraFilesystems    map[string]fsRow `parquet:"extra_filesystems"`
}

var nodeRows = newRowMapper[apigen.NodeMetricsSample, nodeRow]()

var nodeTable = &table[apigen.NodeMetricsSample, nodeRow]{
	name:    "node metrics",
	decode:  apigen.DecodeNodeMetricsSample,
	encode:  func(s *apigen.NodeMetricsSample) []byte { return s.Encode() },
	timeOf:  func(s *apigen.NodeMetricsSample) int64 { return s.Time },
	toRow:   nodeRowFromSample,
	fromRow: nodeSampleFromRow,
	sorting: []parquet.SortingColumn{parquet.Ascending("time")},
}

func nodeRowFromSample(s *apigen.NodeMetricsSample) nodeRow {
	r := nodeRows.toRow(s)
	if len(s.ExtraFilesystems) > 0 {
		r.ExtraFilesystems = make(map[string]fsRow, len(s.ExtraFilesystems))
		for mount, u := range s.ExtraFilesystems {
			r.ExtraFilesystems[mount] = fsRow{TotalBytes: u.TotalBytes, FreeBytes: u.FreeBytes, AvailBytes: u.AvailBytes, InodesTotal: u.InodesTotal, InodesFree: u.InodesFree}
		}
	}
	return r
}

func nodeSampleFromRow(r *nodeRow) *apigen.NodeMetricsSample {
	s := nodeRows.fromRow(r)
	for mount, u := range r.ExtraFilesystems {
		// A file written without the map column reads back with one entry under
		// an empty key on some rows; a real mount point is never empty.
		if mount == "" {
			continue
		}
		if s.ExtraFilesystems == nil {
			s.ExtraFilesystems = make(map[string]apigen.FilesystemUsage, len(r.ExtraFilesystems))
		}
		s.ExtraFilesystems[mount] = apigen.FilesystemUsage{TotalBytes: u.TotalBytes, FreeBytes: u.FreeBytes, AvailBytes: u.AvailBytes, InodesTotal: u.InodesTotal, InodesFree: u.InodesFree}
	}
	return s
}

func toNodeSample(s *metrics.NodeSample, nodeID int32) *apigen.NodeMetricsSample {
	out := &apigen.NodeMetricsSample{Time: s.Time.UnixMilli(), NodeID: nodeID}
	if c := s.CPU; c != nil {
		out.CpuUsageUsec = i64(c.UsageUsec)
		out.CpuUserUsec = i64(c.UserUsec)
		out.CpuSystemUsec = i64(c.SystemUsec)
		out.CpuIowaitUsec = i64(c.IowaitUsec)
		out.CpuStealUsec = i64(c.StealUsec)
		out.CpuCount = i64(c.Count)
	}
	if l := s.Load; l != nil {
		out.Load1 = f64(l.Load1)
		out.Load5 = f64(l.Load5)
		out.Load15 = f64(l.Load15)
	}
	if m := s.Memory; m != nil {
		out.MemTotal = i64(m.Total)
		out.MemAvailable = i64(m.Available)
		out.MemFree = i64(m.Free)
		out.MemCached = i64(m.Cached)
		out.SwapTotal = i64(m.SwapTotal)
		out.SwapFree = i64(m.SwapFree)
	}
	if p := s.CPUPressure; p != nil {
		psi(p.Some, &out.PsiCpuSomeAvg10, &out.PsiCpuSomeAvg60, &out.PsiCpuSomeAvg300, &out.PsiCpuSomeTotalUsec)
		psi(p.Full, &out.PsiCpuFullAvg10, &out.PsiCpuFullAvg60, &out.PsiCpuFullAvg300, &out.PsiCpuFullTotalUsec)
	}
	if p := s.MemoryPressure; p != nil {
		psi(p.Some, &out.PsiMemSomeAvg10, &out.PsiMemSomeAvg60, &out.PsiMemSomeAvg300, &out.PsiMemSomeTotalUsec)
		psi(p.Full, &out.PsiMemFullAvg10, &out.PsiMemFullAvg60, &out.PsiMemFullAvg300, &out.PsiMemFullTotalUsec)
	}
	if p := s.IOPressure; p != nil {
		psi(p.Some, &out.PsiIoSomeAvg10, &out.PsiIoSomeAvg60, &out.PsiIoSomeAvg300, &out.PsiIoSomeTotalUsec)
		psi(p.Full, &out.PsiIoFullAvg10, &out.PsiIoFullAvg60, &out.PsiIoFullAvg300, &out.PsiIoFullTotalUsec)
	}
	if n := s.Net; n != nil {
		out.NetRxBytes = i64(n.RxBytes)
		out.NetRxPackets = i64(n.RxPackets)
		out.NetRxDropped = i64(n.RxDropped)
		out.NetTxBytes = i64(n.TxBytes)
		out.NetTxPackets = i64(n.TxPackets)
		out.NetTxDropped = i64(n.TxDropped)
	}
	if io := s.IO; io != nil {
		out.IoReadBytes = i64(io.ReadBytes)
		out.IoWriteBytes = i64(io.WriteBytes)
		out.IoReadOps = i64(io.ReadOps)
		out.IoWriteOps = i64(io.WriteOps)
	}
	if fs := s.DataFS; fs != nil {
		out.FsDataTotalBytes = i64(fs.TotalBytes)
		out.FsDataFreeBytes = i64(fs.FreeBytes)
		out.FsDataAvailBytes = i64(fs.AvailBytes)
		out.FsDataInodesTotal = i64(fs.InodesTotal)
		out.FsDataInodesFree = i64(fs.InodesFree)
	}
	if len(s.ExtraFS) > 0 {
		out.ExtraFilesystems = make(map[string]apigen.FilesystemUsage, len(s.ExtraFS))
		for mount, fs := range s.ExtraFS {
			out.ExtraFilesystems[mount] = apigen.FilesystemUsage{
				TotalBytes:  int64(fs.TotalBytes),
				FreeBytes:   int64(fs.FreeBytes),
				AvailBytes:  int64(fs.AvailBytes),
				InodesTotal: int64(fs.InodesTotal),
				InodesFree:  int64(fs.InodesFree),
			}
		}
	}
	return out
}

type NodeStore struct {
	dir    string
	nodeID int32
	sink   *sink[apigen.NodeMetricsSample, nodeRow]
	done   chan struct{}

	mu   sync.Mutex
	cur  *apigen.NodeMetricsSample
	prev *apigen.NodeMetricsSample
}

func StartNode(ctx context.Context, dir string, nodeID int32) *NodeStore {
	ctx = logu.AddTag(ctx, "Metrics")
	k := startSink(ctx, nodeTable, dir, nodeID)
	return &NodeStore{dir: dir, nodeID: nodeID, sink: k, done: k.done}
}

func (s *NodeStore) ConsumeNode(ctx context.Context, sample metrics.NodeSample) {
	next := toNodeSample(&sample, s.nodeID)
	s.mu.Lock()
	s.prev, s.cur = s.cur, next
	s.mu.Unlock()
	s.sink.enqueue(ctx, []*apigen.NodeMetricsSample{next})
}

func (s *NodeStore) LatestResponse() *apigen.NodeMetricsLatestResponse {
	s.mu.Lock()
	prev, cur := s.prev, s.cur
	s.mu.Unlock()
	out := &apigen.NodeMetricsLatestResponse{}
	if cur == nil {
		return out
	}
	entry := apigen.NodeMetricsLatestEntry{Sample: *cur}
	if prev != nil {
		entry.Rates = counterRates(prev, cur, prev.Time, cur.Time, NodeFields)
	}
	out.Entries = []apigen.NodeMetricsLatestEntry{entry}
	return out
}

type NodeRollupResult struct {
	Start   time.Time
	Step    time.Duration
	Buckets int
	Series  []NodeSeries
	Scanned int
}

type NodeSeries struct {
	Field  NodeField
	Values []float64
}

func NodeRollup(ctx context.Context, dir string, nodeID int32, from, to time.Time, step time.Duration, fields []NodeField) (NodeRollupResult, error) {
	plan, err := planRollup(from, to, step)
	if err != nil {
		return NodeRollupResult{}, err
	}
	if len(fields) == 0 {
		fields = NodeFields
	}
	spec := scanSpec[apigen.NodeMetricsSample]{
		timeRange: plan.scan,
		acceptPayload: func(payload []byte) bool {
			t, _ := peekSample(payload)
			return plan.scan.contains(t)
		},
		match: func(s *apigen.NodeMetricsSample) bool { return s.NodeID == nodeID },
	}
	acc := newRollupAcc[apigen.NodeMetricsSample](len(fields))
	scanned := 0
	err = scan(ctx, nodeTable, dir, nodeID, spec, func(s *apigen.NodeMetricsSample) bool {
		if acc.prev != nil && s.Time <= acc.prevTime {
			return true
		}
		scanned++
		acc.fold(s, s.Time, fields, plan.grid)
		return true
	})
	if err != nil {
		return NodeRollupResult{}, err
	}
	res := NodeRollupResult{Start: plan.start, Step: plan.step, Buckets: plan.buckets, Scanned: scanned}
	for i, f := range fields {
		if values := acc.values(i, plan.buckets); values != nil {
			res.Series = append(res.Series, NodeSeries{Field: f, Values: values})
		}
	}
	return res, nil
}

func (s *NodeStore) QueryResponse(ctx context.Context, req *apigen.NodeMetricsQueryRequest) (*apigen.NodeMetricsQueryResponse, error) {
	started := time.Now()
	from, to, err := ResolveRange(req.TimeStart, req.TimeEnd, started)
	if err != nil {
		return nil, err
	}
	fields, err := NodeRequestFields(req.Fields)
	if err != nil {
		return nil, err
	}
	res, err := NodeRollup(ctx, s.dir, s.nodeID, from, to, time.Duration(req.StepMs)*time.Millisecond, fields)
	if err != nil {
		return nil, err
	}
	out := &apigen.NodeMetricsQueryResponse{
		TimeStart:   res.Start,
		StepMs:      res.Step.Milliseconds(),
		Buckets:     int32(res.Buckets),
		ScannedRows: int64(res.Scanned),
		TookMs:      int32(time.Since(started).Milliseconds()),
	}
	for _, ser := range res.Series {
		out.Series = append(out.Series, apigen.NodeMetricsSeries{
			NodeID: uint64(s.nodeID),
			Field:  ser.Field.Name,
			Kind:   int32(ser.Field.Kind),
			Values: ser.Values,
		})
	}
	return out, nil
}
