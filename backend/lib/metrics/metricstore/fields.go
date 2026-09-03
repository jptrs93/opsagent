package metricstore

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

type Kind int

const (
	Counter Kind = iota
	Gauge
	Average
)

type Field struct {
	Name  string
	Kind  Kind
	Int   func(*apigen.MetricsSample) *int64
	Float func(*apigen.MetricsSample) *float64
}

type sample = apigen.MetricsSample

var Fields = []Field{
	{Name: "cpu_usage_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.CpuUsageUsec }},
	{Name: "cpu_user_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.CpuUserUsec }},
	{Name: "cpu_system_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.CpuSystemUsec }},
	{Name: "cpu_throttled_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.CpuThrottledUsec }},
	{Name: "cpu_nr_throttled", Kind: Counter, Int: func(s *sample) *int64 { return s.CpuNrThrottled }},
	{Name: "mem_current", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemCurrent }},
	{Name: "mem_peak", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemPeak }},
	{Name: "mem_anon", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemAnon }},
	{Name: "mem_file", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemFile }},
	{Name: "mem_kernel", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemKernel }},
	{Name: "mem_shmem", Kind: Gauge, Int: func(s *sample) *int64 { return s.MemShmem }},
	{Name: "mem_oom", Kind: Counter, Int: func(s *sample) *int64 { return s.MemOom }},
	{Name: "mem_oom_kill", Kind: Counter, Int: func(s *sample) *int64 { return s.MemOomKill }},
	{Name: "io_read_bytes", Kind: Counter, Int: func(s *sample) *int64 { return s.IoReadBytes }},
	{Name: "io_write_bytes", Kind: Counter, Int: func(s *sample) *int64 { return s.IoWriteBytes }},
	{Name: "io_read_ops", Kind: Counter, Int: func(s *sample) *int64 { return s.IoReadOps }},
	{Name: "io_write_ops", Kind: Counter, Int: func(s *sample) *int64 { return s.IoWriteOps }},
	{Name: "pids", Kind: Gauge, Int: func(s *sample) *int64 { return s.Pids }},
	{Name: "psi_cpu_some_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuSomeAvg10 }},
	{Name: "psi_cpu_some_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuSomeAvg60 }},
	{Name: "psi_cpu_some_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuSomeAvg300 }},
	{Name: "psi_cpu_some_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiCpuSomeTotalUsec }},
	{Name: "psi_cpu_full_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuFullAvg10 }},
	{Name: "psi_cpu_full_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuFullAvg60 }},
	{Name: "psi_cpu_full_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiCpuFullAvg300 }},
	{Name: "psi_cpu_full_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiCpuFullTotalUsec }},
	{Name: "psi_mem_some_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemSomeAvg10 }},
	{Name: "psi_mem_some_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemSomeAvg60 }},
	{Name: "psi_mem_some_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemSomeAvg300 }},
	{Name: "psi_mem_some_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiMemSomeTotalUsec }},
	{Name: "psi_mem_full_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemFullAvg10 }},
	{Name: "psi_mem_full_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemFullAvg60 }},
	{Name: "psi_mem_full_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiMemFullAvg300 }},
	{Name: "psi_mem_full_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiMemFullTotalUsec }},
	{Name: "psi_io_some_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoSomeAvg10 }},
	{Name: "psi_io_some_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoSomeAvg60 }},
	{Name: "psi_io_some_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoSomeAvg300 }},
	{Name: "psi_io_some_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiIoSomeTotalUsec }},
	{Name: "psi_io_full_avg10", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoFullAvg10 }},
	{Name: "psi_io_full_avg60", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoFullAvg60 }},
	{Name: "psi_io_full_avg300", Kind: Average, Float: func(s *sample) *float64 { return s.PsiIoFullAvg300 }},
	{Name: "psi_io_full_total_usec", Kind: Counter, Int: func(s *sample) *int64 { return s.PsiIoFullTotalUsec }},
	{Name: "net_rx_bytes", Kind: Counter, Int: func(s *sample) *int64 { return s.NetRxBytes }},
	{Name: "net_rx_packets", Kind: Counter, Int: func(s *sample) *int64 { return s.NetRxPackets }},
	{Name: "net_rx_dropped", Kind: Counter, Int: func(s *sample) *int64 { return s.NetRxDropped }},
	{Name: "net_tx_bytes", Kind: Counter, Int: func(s *sample) *int64 { return s.NetTxBytes }},
	{Name: "net_tx_packets", Kind: Counter, Int: func(s *sample) *int64 { return s.NetTxPackets }},
	{Name: "net_tx_dropped", Kind: Counter, Int: func(s *sample) *int64 { return s.NetTxDropped }},
	{Name: "tcp_established", Kind: Gauge, Int: func(s *sample) *int64 { return s.TcpEstablished }},
	{Name: "tcp_listen", Kind: Gauge, Int: func(s *sample) *int64 { return s.TcpListen }},
	{Name: "tcp_time_wait", Kind: Gauge, Int: func(s *sample) *int64 { return s.TcpTimeWait }},
	{Name: "tcp_close_wait", Kind: Gauge, Int: func(s *sample) *int64 { return s.TcpCloseWait }},
	{Name: "tcp_other", Kind: Gauge, Int: func(s *sample) *int64 { return s.TcpOther }},
	{Name: "open_fds", Kind: Gauge, Int: func(s *sample) *int64 { return s.OpenFds }},
}

func FieldByName(name string) (Field, bool) {
	for _, f := range Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func (f Field) Value(s *apigen.MetricsSample) (float64, bool) {
	if f.Int != nil {
		if v := f.Int(s); v != nil {
			return float64(*v), true
		}
		return 0, false
	}
	if v := f.Float(s); v != nil {
		return *v, true
	}
	return 0, false
}

func Rate(prev, cur *apigen.MetricsSample, f Field) (float64, bool) {
	if f.Kind != Counter || f.Int == nil || Key(prev) != Key(cur) || cur.Time <= prev.Time {
		return 0, false
	}
	a, b := f.Int(prev), f.Int(cur)
	if a == nil || b == nil || *b < *a {
		return 0, false
	}
	return float64(*b-*a) / (float64(cur.Time-prev.Time) / 1000), true
}

func GroupByKey(sorted []*apigen.MetricsSample) [][]*apigen.MetricsSample {
	var groups [][]*apigen.MetricsSample
	start := 0
	for i := 1; i <= len(sorted); i++ {
		if i == len(sorted) || Key(sorted[i]) != Key(sorted[start]) {
			groups = append(groups, sorted[start:i])
			start = i
		}
	}
	return groups
}
