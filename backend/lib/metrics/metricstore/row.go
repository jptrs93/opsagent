package metricstore

import (
	"cmp"
	"reflect"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/parquet-go/parquet-go"
)

type row struct {
	Time                int64    `parquet:"time,timestamp(millisecond)"`
	DeploymentID        int32    `parquet:"deployment_id"`
	ScheduledInstanceID int32    `parquet:"scheduled_instance_id"`
	Ordinal             int32    `parquet:"ordinal"`
	SpecVersion         int32    `parquet:"spec_version"`
	Run                 int32    `parquet:"run"`
	NodeID              int32    `parquet:"node_id"`
	Terminal            bool     `parquet:"terminal"`
	CpuUsageUsec        *int64   `parquet:"cpu_usage_usec"`
	CpuUserUsec         *int64   `parquet:"cpu_user_usec"`
	CpuSystemUsec       *int64   `parquet:"cpu_system_usec"`
	CpuThrottledUsec    *int64   `parquet:"cpu_throttled_usec"`
	CpuNrThrottled      *int64   `parquet:"cpu_nr_throttled"`
	MemCurrent          *int64   `parquet:"mem_current"`
	MemPeak             *int64   `parquet:"mem_peak"`
	MemAnon             *int64   `parquet:"mem_anon"`
	MemFile             *int64   `parquet:"mem_file"`
	MemKernel           *int64   `parquet:"mem_kernel"`
	MemShmem            *int64   `parquet:"mem_shmem"`
	MemOom              *int64   `parquet:"mem_oom"`
	MemOomKill          *int64   `parquet:"mem_oom_kill"`
	IoReadBytes         *int64   `parquet:"io_read_bytes"`
	IoWriteBytes        *int64   `parquet:"io_write_bytes"`
	IoReadOps           *int64   `parquet:"io_read_ops"`
	IoWriteOps          *int64   `parquet:"io_write_ops"`
	Pids                *int64   `parquet:"pids"`
	PsiCpuSomeAvg10     *float64 `parquet:"psi_cpu_some_avg10"`
	PsiCpuSomeAvg60     *float64 `parquet:"psi_cpu_some_avg60"`
	PsiCpuSomeAvg300    *float64 `parquet:"psi_cpu_some_avg300"`
	PsiCpuSomeTotalUsec *int64   `parquet:"psi_cpu_some_total_usec"`
	PsiCpuFullAvg10     *float64 `parquet:"psi_cpu_full_avg10"`
	PsiCpuFullAvg60     *float64 `parquet:"psi_cpu_full_avg60"`
	PsiCpuFullAvg300    *float64 `parquet:"psi_cpu_full_avg300"`
	PsiCpuFullTotalUsec *int64   `parquet:"psi_cpu_full_total_usec"`
	PsiMemSomeAvg10     *float64 `parquet:"psi_mem_some_avg10"`
	PsiMemSomeAvg60     *float64 `parquet:"psi_mem_some_avg60"`
	PsiMemSomeAvg300    *float64 `parquet:"psi_mem_some_avg300"`
	PsiMemSomeTotalUsec *int64   `parquet:"psi_mem_some_total_usec"`
	PsiMemFullAvg10     *float64 `parquet:"psi_mem_full_avg10"`
	PsiMemFullAvg60     *float64 `parquet:"psi_mem_full_avg60"`
	PsiMemFullAvg300    *float64 `parquet:"psi_mem_full_avg300"`
	PsiMemFullTotalUsec *int64   `parquet:"psi_mem_full_total_usec"`
	PsiIoSomeAvg10      *float64 `parquet:"psi_io_some_avg10"`
	PsiIoSomeAvg60      *float64 `parquet:"psi_io_some_avg60"`
	PsiIoSomeAvg300     *float64 `parquet:"psi_io_some_avg300"`
	PsiIoSomeTotalUsec  *int64   `parquet:"psi_io_some_total_usec"`
	PsiIoFullAvg10      *float64 `parquet:"psi_io_full_avg10"`
	PsiIoFullAvg60      *float64 `parquet:"psi_io_full_avg60"`
	PsiIoFullAvg300     *float64 `parquet:"psi_io_full_avg300"`
	PsiIoFullTotalUsec  *int64   `parquet:"psi_io_full_total_usec"`
	NetRxBytes          *int64   `parquet:"net_rx_bytes"`
	NetRxPackets        *int64   `parquet:"net_rx_packets"`
	NetRxDropped        *int64   `parquet:"net_rx_dropped"`
	NetTxBytes          *int64   `parquet:"net_tx_bytes"`
	NetTxPackets        *int64   `parquet:"net_tx_packets"`
	NetTxDropped        *int64   `parquet:"net_tx_dropped"`
	TcpEstablished      *int64   `parquet:"tcp_established"`
	TcpListen           *int64   `parquet:"tcp_listen"`
	TcpTimeWait         *int64   `parquet:"tcp_time_wait"`
	TcpCloseWait        *int64   `parquet:"tcp_close_wait"`
	TcpOther            *int64   `parquet:"tcp_other"`
	OpenFds             *int64   `parquet:"open_fds"`
}

func sortingColumns() []parquet.SortingColumn {
	return []parquet.SortingColumn{
		parquet.Ascending("deployment_id"),
		parquet.Ascending("scheduled_instance_id"),
		parquet.Ascending("ordinal"),
		parquet.Ascending("spec_version"),
		parquet.Ascending("run"),
		parquet.Ascending("time"),
	}
}

var rowFieldPairs [][2]int

func init() {
	rt := reflect.TypeFor[row]()
	st := reflect.TypeFor[apigen.MetricsSample]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if sf, ok := st.FieldByName(f.Name); ok && sf.Type == f.Type {
			rowFieldPairs = append(rowFieldPairs, [2]int{i, sf.Index[0]})
		}
	}
}

func rowFromSample(s *apigen.MetricsSample) row {
	var r row
	rv := reflect.ValueOf(&r).Elem()
	sv := reflect.ValueOf(s).Elem()
	for _, p := range rowFieldPairs {
		rv.Field(p[0]).Set(sv.Field(p[1]))
	}
	return r
}

func sampleFromRow(r *row) *apigen.MetricsSample {
	s := &apigen.MetricsSample{}
	rv := reflect.ValueOf(r).Elem()
	sv := reflect.ValueOf(s).Elem()
	for _, p := range rowFieldPairs {
		sv.Field(p[1]).Set(rv.Field(p[0]))
	}
	return s
}

func Key(s *apigen.MetricsSample) metrics.TargetKey {
	return metrics.TargetKey{
		DeploymentID:        s.DeploymentID,
		ScheduledInstanceID: s.ScheduledInstanceID,
		Ordinal:             s.Ordinal,
		SpecVersion:         s.SpecVersion,
		Run:                 s.Run,
	}
}

func CompareKey(a, b metrics.TargetKey) int {
	if c := cmp.Compare(a.DeploymentID, b.DeploymentID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.ScheduledInstanceID, b.ScheduledInstanceID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Ordinal, b.Ordinal); c != 0 {
		return c
	}
	if c := cmp.Compare(a.SpecVersion, b.SpecVersion); c != 0 {
		return c
	}
	return cmp.Compare(a.Run, b.Run)
}

func compareKeyTime(a, b *apigen.MetricsSample) int {
	if c := CompareKey(Key(a), Key(b)); c != 0 {
		return c
	}
	return cmp.Compare(a.Time, b.Time)
}

func toSample(s *metrics.Sample, nodeID int32) *apigen.MetricsSample {
	out := &apigen.MetricsSample{
		Time:                s.Time.UnixMilli(),
		DeploymentID:        s.Key.DeploymentID,
		ScheduledInstanceID: s.Key.ScheduledInstanceID,
		Ordinal:             s.Key.Ordinal,
		SpecVersion:         s.Key.SpecVersion,
		Run:                 s.Key.Run,
		NodeID:              nodeID,
		Terminal:            s.Terminal,
	}
	if c := s.Cgroup.CPU; c != nil {
		out.CpuUsageUsec = i64(c.UsageUsec)
		out.CpuUserUsec = i64(c.UserUsec)
		out.CpuSystemUsec = i64(c.SystemUsec)
		out.CpuThrottledUsec = i64(c.ThrottledUsec)
		out.CpuNrThrottled = i64(c.NrThrottled)
	}
	if m := s.Cgroup.Memory; m != nil {
		out.MemCurrent = i64(m.Current)
		if m.Peak != nil {
			out.MemPeak = i64(*m.Peak)
		}
		out.MemAnon = i64(m.Anon)
		out.MemFile = i64(m.File)
		out.MemKernel = i64(m.Kernel)
		out.MemShmem = i64(m.Shmem)
		out.MemOom = i64(m.OOM)
		out.MemOomKill = i64(m.OOMKill)
	}
	if io := s.Cgroup.IO; io != nil {
		out.IoReadBytes = i64(io.ReadBytes)
		out.IoWriteBytes = i64(io.WriteBytes)
		out.IoReadOps = i64(io.ReadOps)
		out.IoWriteOps = i64(io.WriteOps)
	}
	if p := s.Cgroup.Pids; p != nil {
		out.Pids = i64(*p)
	}
	if p := s.Cgroup.CPUPressure; p != nil {
		psi(p.Some, &out.PsiCpuSomeAvg10, &out.PsiCpuSomeAvg60, &out.PsiCpuSomeAvg300, &out.PsiCpuSomeTotalUsec)
		psi(p.Full, &out.PsiCpuFullAvg10, &out.PsiCpuFullAvg60, &out.PsiCpuFullAvg300, &out.PsiCpuFullTotalUsec)
	}
	if p := s.Cgroup.MemoryPressure; p != nil {
		psi(p.Some, &out.PsiMemSomeAvg10, &out.PsiMemSomeAvg60, &out.PsiMemSomeAvg300, &out.PsiMemSomeTotalUsec)
		psi(p.Full, &out.PsiMemFullAvg10, &out.PsiMemFullAvg60, &out.PsiMemFullAvg300, &out.PsiMemFullTotalUsec)
	}
	if p := s.Cgroup.IOPressure; p != nil {
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
		out.TcpEstablished = i64(n.TCP.Established)
		out.TcpListen = i64(n.TCP.Listen)
		out.TcpTimeWait = i64(n.TCP.TimeWait)
		out.TcpCloseWait = i64(n.TCP.CloseWait)
		out.TcpOther = i64(n.TCP.Other)
	}
	if s.OpenFDs != nil {
		out.OpenFds = i64(*s.OpenFDs)
	}
	return out
}

func psi(line metrics.PressureLine, avg10, avg60, avg300 **float64, total **int64) {
	*avg10 = f64(line.Avg10)
	*avg60 = f64(line.Avg60)
	*avg300 = f64(line.Avg300)
	*total = i64(line.TotalUsec)
}

func i64(v uint64) *int64 {
	x := int64(v)
	return &x
}

func f64(v float64) *float64 {
	return &v
}
