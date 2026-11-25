package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	cpu := parseProcStat([]byte(`cpu  1000 50 300 9000 120 10 20 30 5 0
cpu0 500 25 150 4500 60 5 10 15 2 0
cpu1 500 25 150 4500 60 5 10 15 3 0
intr 12345 0 0
ctxt 999
`))
	want := HostCPU{UsageUsec: 14100000, UserUsec: 10500000, SystemUsec: 3300000, IowaitUsec: 1200000, StealUsec: 300000, Count: 2}
	if cpu == nil || *cpu != want {
		t.Fatalf("got %+v want %+v", cpu, want)
	}
	if parseProcStat([]byte("intr 1 2 3\n")) != nil {
		t.Fatal("a file without the aggregate cpu line must give no sample")
	}
}

func TestParseLoadAvg(t *testing.T) {
	l := parseLoadAvg([]byte("0.52 1.25 2.00 3/412 99123\n"))
	if l == nil || *l != (LoadAvg{Load1: 0.52, Load5: 1.25, Load15: 2}) {
		t.Fatalf("got %+v", l)
	}
	if parseLoadAvg([]byte("garbage")) != nil {
		t.Fatal("a short file must give no sample")
	}
}

func TestParseMeminfo(t *testing.T) {
	m := parseMeminfo([]byte(`MemTotal:       16000000 kB
MemFree:         1000000 kB
MemAvailable:    9000000 kB
Buffers:          200000 kB
Cached:          6000000 kB
SwapCached:            0 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
HugePages_Total:       0
`))
	const kb = 1024
	want := HostMemory{Total: 16000000 * kb, Available: 9000000 * kb, Free: 1000000 * kb, Cached: 6200000 * kb, SwapTotal: 2000000 * kb, SwapFree: 1500000 * kb}
	if m == nil || *m != want {
		t.Fatalf("got %+v want %+v", m, want)
	}
	if parseMeminfo([]byte("Cached: 5 kB\n")) != nil {
		t.Fatal("a file without MemTotal must give no sample")
	}
}

func TestParseHostNetDevCountsOnlyChosenInterfaces(t *testing.T) {
	b := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  999000    9000    0    0    0     0          0         0   999000    9000    0    0    0     0       0          0
  eth0:   10000     100    0    2    0     0          0         0    20000     200    0    3    0     0       0          0
od24s0:    7000      70    0    0    0     0          0         0     8000      80    0    0    0     0       0          0
  eth1:    1000      10    0    0    0     0          0         0     2000      20    0    0    0     0       0          0
`)
	physical := func(name string) bool { return name == "eth0" || name == "eth1" }
	n := parseHostNetDev(b, physical)
	want := HostNet{RxBytes: 11000, RxPackets: 110, RxDropped: 2, TxBytes: 22000, TxPackets: 220, TxDropped: 3}
	if n == nil || *n != want {
		t.Fatalf("got %+v want %+v", n, want)
	}
	if parseHostNetDev(b, func(string) bool { return false }) != nil {
		t.Fatal("no chosen interface must give no sample, not zeros")
	}
}

func TestParseDiskstatsSumsWholeDevices(t *testing.T) {
	b := []byte(`   8       0 sda 100 0 2000 50 300 0 8000 70 0 90 120 0 0 0 0
   8       1 sda1 90 0 1800 45 290 0 7900 65 0 85 110 0 0 0 0
 259       0 nvme0n1 10 0 400 5 20 0 1600 9 0 12 14 0 0 0 0
   7       0 loop0 5 0 80 1 0 0 0 0 0 1 1 0 0 0 0
`)
	whole := func(name string) bool { return name == "sda" || name == "nvme0n1" }
	s := parseDiskstats(b, whole)
	want := IOStats{ReadBytes: 2400 * 512, WriteBytes: 9600 * 512, ReadOps: 110, WriteOps: 320}
	if s == nil || *s != want {
		t.Fatalf("got %+v want %+v", s, want)
	}
	if parseDiskstats(b, func(string) bool { return false }) != nil {
		t.Fatal("no chosen device must give no sample, not zeros")
	}
}

func TestReadFilesystemsReportsTheDataFilesystemAndNoExtraOnOneDevice(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "opendeploy")
	volumes := filepath.Join(root, "opendeploy-volumes")
	for _, dir := range []string{data, volumes} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	linked := filepath.Join(root, "opendeploy-nix")
	if err := os.Symlink(volumes, linked); err != nil {
		t.Fatal(err)
	}
	usage, extra, err := readFilesystems(NodeRoots{Data: data, Other: []string{volumes, linked, filepath.Join(root, "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.TotalBytes == 0 || usage.FreeBytes > usage.TotalBytes || usage.AvailBytes > usage.FreeBytes {
		t.Fatalf("data filesystem = %+v, want a total with free and available inside it", usage)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %v, want none when every root shares the data filesystem", extra)
	}
	if usage, extra, err := readFilesystems(NodeRoots{}); usage != nil || extra != nil || err != nil {
		t.Fatalf("no data root = %v %v %v, want nothing", usage, extra, err)
	}
}

func TestMountPointStopsWhereTheDeviceChanges(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	dev, err := deviceOf(deep)
	if err != nil {
		t.Fatal(err)
	}
	got := mountPoint(deep, dev)
	gotDev, err := deviceOf(got)
	if err != nil || gotDev != dev {
		t.Fatalf("mount point %s is on device %d (err %v), want %d", got, gotDev, err, dev)
	}
	if parent := filepath.Dir(got); parent != got {
		if parentDev, err := deviceOf(parent); err == nil && parentDev == dev {
			t.Fatalf("mount point %s has a parent on the same device", got)
		}
	}
	if other := mountPoint(deep, dev+1); other != deep {
		t.Fatalf("mount point for another device = %s, want the path itself", other)
	}
}

func TestReadNodeAlwaysCarriesTheTimeAndTheDataFilesystem(t *testing.T) {
	now := time.Now()
	s, _ := ReadNode(NodeRoots{Data: t.TempDir()}, now)
	if !s.Time.Equal(now) || s.DataFS == nil || s.DataFS.TotalBytes == 0 {
		t.Fatalf("sample = %+v, want the time and the data filesystem", s)
	}
}
