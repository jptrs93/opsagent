package metrics

import (
	"context"
	"testing"
	"time"
)

func TestParseCPUStat(t *testing.T) {
	s := parseCPUStat([]byte("usage_usec 1234567\nuser_usec 1000000\nsystem_usec 234567\nnr_periods 10\nnr_throttled 2\nthrottled_usec 500\n"))
	if s.UsageUsec != 1234567 || s.UserUsec != 1000000 || s.SystemUsec != 234567 || s.NrThrottled != 2 || s.ThrottledUsec != 500 {
		t.Fatalf("unexpected cpu stats %+v", *s)
	}
}

func TestParseMemory(t *testing.T) {
	m := MemoryStats{Current: 42}
	parseMemoryStat([]byte("anon 10\nfile 20\nkernel 5\nshmem 3\nsock 0\n"), &m)
	parseMemoryEvents([]byte("low 0\nhigh 0\nmax 0\noom 1\noom_kill 1\noom_group_kill 0\n"), &m)
	want := MemoryStats{Current: 42, Anon: 10, File: 20, Kernel: 5, Shmem: 3, OOM: 1, OOMKill: 1}
	if m != want {
		t.Fatalf("got %+v want %+v", m, want)
	}
}

func TestParseIOStatSumsDevices(t *testing.T) {
	s := parseIOStat([]byte("8:0 rbytes=100 wbytes=200 rios=1 wios=2 dbytes=0 dios=0\n259:0 rbytes=50 wbytes=25 rios=3 wios=4 dbytes=0 dios=0\n"))
	want := IOStats{ReadBytes: 150, WriteBytes: 225, ReadOps: 4, WriteOps: 6}
	if *s != want {
		t.Fatalf("got %+v want %+v", *s, want)
	}
}

func TestParsePressure(t *testing.T) {
	p := parsePressure([]byte("some avg10=1.50 avg60=0.75 avg300=0.10 total=123456\nfull avg10=0.20 avg60=0.10 avg300=0.00 total=789\n"))
	if p.Some != (PressureLine{Avg10: 1.5, Avg60: 0.75, Avg300: 0.1, TotalUsec: 123456}) {
		t.Fatalf("unexpected some line %+v", p.Some)
	}
	if p.Full != (PressureLine{Avg10: 0.2, Avg60: 0.1, TotalUsec: 789}) {
		t.Fatalf("unexpected full line %+v", p.Full)
	}
	if p := parsePressure([]byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")); p.Full != (PressureLine{}) {
		t.Fatalf("missing full line should be zero, got %+v", p.Full)
	}
}

func TestParseNetDevSkipsLoopback(t *testing.T) {
	b := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  999000    9000    0    0    0     0          0         0   999000    9000    0    0    0     0       0          0
  eth0:   10000     100    0    2    0     0          0         0    20000     200    0    3    0     0       0          0
  eth1:    1000      10    0    0    0     0          0         0     2000      20    0    0    0     0       0          0
`)
	m := parseNetDev(b)
	if m.RxBytes != 11000 || m.RxPackets != 110 || m.RxDropped != 2 || m.TxBytes != 22000 || m.TxPackets != 220 || m.TxDropped != 3 {
		t.Fatalf("unexpected net dev %+v", *m)
	}
}

func TestParseTCPStates(t *testing.T) {
	b := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 0100007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 20 4 30 10 -1
   2: 0100007F:1F90 0100007F:C351 06 00000000:00000000 03:00001234 00000000     0        0 0 3 0000000000000000
   3: 0100007F:1F90 0100007F:C352 08 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 20 4 30 10 -1
   4: 0100007F:1F90 0100007F:C353 02 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 20 4 30 10 -1
`)
	var s TCPStates
	parseTCP(b, &s)
	want := TCPStates{Established: 1, Listen: 1, TimeWait: 1, CloseWait: 1, Other: 1}
	if s != want {
		t.Fatalf("got %+v want %+v", s, want)
	}
}

type recordingConsumer struct {
	batches [][]Sample
}

func (c *recordingConsumer) Consume(_ context.Context, samples []Sample) {
	c.batches = append(c.batches, samples)
}

func TestRegisterAndClose(t *testing.T) {
	ctx := context.Background()
	consumer := &recordingConsumer{}
	s := &Sampler{ctx: ctx, consumer: consumer}
	key := TargetKey{DeploymentID: 1, ScheduledInstanceID: 2, SpecVersion: 3, Run: 4}
	reg := s.Register(ctx, TargetSpec{Key: key, CgroupsPath: "/opendeploy/1-p2-v3-r4"})
	if _, ok := s.targets[key]; !ok {
		t.Fatal("target not registered")
	}
	now := time.Now()
	samples := s.sampleAll(ctx, now)
	if len(samples) != 1 || samples[0].Key != key || samples[0].Terminal || !samples[0].Time.Equal(now) {
		t.Fatalf("unexpected samples %+v", samples)
	}

	reg.Close()
	reg.Close()
	var nilReg *Registration
	nilReg.Close()
	if len(s.targets) != 0 {
		t.Fatal("target not deregistered")
	}
	if len(consumer.batches) != 1 || len(consumer.batches[0]) != 1 {
		t.Fatalf("expected one terminal batch of one sample, got %+v", consumer.batches)
	}
	if final := consumer.batches[0][0]; !final.Terminal || final.Key != key {
		t.Fatalf("unexpected terminal sample %+v", final)
	}
}

func TestCloseBeforeRunDeliversNothing(t *testing.T) {
	s := &Sampler{}
	reg := s.Register(context.Background(), TargetSpec{Key: TargetKey{DeploymentID: 1}})
	reg.Close()
	if len(s.targets) != 0 {
		t.Fatal("target not deregistered")
	}
}

func TestCloseIgnoresReplacedRegistration(t *testing.T) {
	s := &Sampler{}
	key := TargetKey{DeploymentID: 1, Run: 1}
	old := s.Register(context.Background(), TargetSpec{Key: key})
	s.Register(context.Background(), TargetSpec{Key: key})
	old.Close()
	if _, ok := s.targets[key]; !ok {
		t.Fatal("closing a superseded registration removed its replacement")
	}
}

func TestNextTickAlignsToBoundary(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 15, 42, 123, time.UTC)
	if got, want := nextTick(now, 30*time.Second), time.Date(2026, 9, 3, 10, 16, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	onBoundary := time.Date(2026, 9, 3, 10, 16, 0, 0, time.UTC)
	if got, want := nextTick(onBoundary, 30*time.Second), onBoundary.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	local := now.In(time.FixedZone("x", 7*3600+1800))
	if got := nextTick(local, 30*time.Second); !got.Equal(time.Date(2026, 9, 3, 10, 16, 0, 0, time.UTC)) {
		t.Fatalf("local zone changed alignment: %s", got)
	}
}
