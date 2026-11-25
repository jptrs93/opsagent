package metrics

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/goutil/logu"
)

const userHZ = 100

type NodeSample struct {
	Time           time.Time
	CPU            *HostCPU
	Load           *LoadAvg
	Memory         *HostMemory
	CPUPressure    *Pressure
	MemoryPressure *Pressure
	IOPressure     *Pressure
	Net            *HostNet
	IO             *IOStats
	DataFS         *FSUsage
	ExtraFS        map[string]FSUsage
}

type HostCPU struct {
	UsageUsec  uint64
	UserUsec   uint64
	SystemUsec uint64
	IowaitUsec uint64
	StealUsec  uint64
	Count      uint64
}

type LoadAvg struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

type HostMemory struct {
	Total     uint64
	Available uint64
	Free      uint64
	Cached    uint64
	SwapTotal uint64
	SwapFree  uint64
}

type HostNet struct {
	RxBytes   uint64
	RxPackets uint64
	RxDropped uint64
	TxBytes   uint64
	TxPackets uint64
	TxDropped uint64
}

type FSUsage struct {
	TotalBytes  uint64
	FreeBytes   uint64
	AvailBytes  uint64
	InodesTotal uint64
	InodesFree  uint64
}

type NodeRoots struct {
	Data  string
	Other []string
}

type NodeConsumer interface {
	ConsumeNode(ctx context.Context, sample NodeSample)
}

func RunNode(ctx context.Context, interval time.Duration, roots NodeRoots, consumer NodeConsumer) {
	ctx = logu.AddTag(ctx, "Metrics")
	failing := false
	sample := func() {
		s, err := ReadNode(roots, time.Now())
		if err != nil && !failing {
			slog.DebugContext(ctx, "sampling node metrics failed", "err", err)
		}
		failing = err != nil
		consumer.ConsumeNode(ctx, s)
	}
	sample()
	timer := time.NewTimer(time.Until(nextTick(time.Now(), interval)))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			sample()
			timer.Reset(time.Until(nextTick(time.Now(), interval)))
		}
	}
}

func ReadNode(roots NodeRoots, now time.Time) (NodeSample, error) {
	sample := NodeSample{Time: now}
	var firstErr error
	read := func(path string) ([]byte, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil, false
		}
		return b, true
	}
	if b, ok := read("/proc/stat"); ok {
		sample.CPU = parseProcStat(b)
	}
	if b, ok := read("/proc/loadavg"); ok {
		sample.Load = parseLoadAvg(b)
	}
	if b, ok := read("/proc/meminfo"); ok {
		sample.Memory = parseMeminfo(b)
	}
	if b, ok := read("/proc/pressure/cpu"); ok {
		sample.CPUPressure = parsePressure(b)
	}
	if b, ok := read("/proc/pressure/memory"); ok {
		sample.MemoryPressure = parsePressure(b)
	}
	if b, ok := read("/proc/pressure/io"); ok {
		sample.IOPressure = parsePressure(b)
	}
	if b, ok := read("/proc/net/dev"); ok {
		sample.Net = parseHostNetDev(b, physicalNetDevice)
	}
	if b, ok := read("/proc/diskstats"); ok {
		sample.IO = parseDiskstats(b, physicalBlockDevice)
	}
	var err error
	sample.DataFS, sample.ExtraFS, err = readFilesystems(roots)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return sample, firstErr
}

func physicalNetDevice(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "device"))
	return err == nil
}

func physicalBlockDevice(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/block", name, "device"))
	return err == nil
}

func readFilesystems(roots NodeRoots) (*FSUsage, map[string]FSUsage, error) {
	if roots.Data == "" {
		return nil, nil, nil
	}
	dataDev, err := deviceOf(roots.Data)
	if err != nil {
		return nil, nil, err
	}
	data, err := statFS(roots.Data)
	if err != nil {
		return nil, nil, err
	}
	seen := map[uint64]bool{dataDev: true}
	var extra map[string]FSUsage
	for _, root := range roots.Other {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		dev, err := deviceOf(resolved)
		if err != nil || seen[dev] {
			continue
		}
		seen[dev] = true
		usage, err := statFS(resolved)
		if err != nil {
			continue
		}
		if extra == nil {
			extra = make(map[string]FSUsage)
		}
		extra[mountPoint(resolved, dev)] = usage
	}
	return &data, extra, nil
}

func mountPoint(path string, dev uint64) string {
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		parentDev, err := deviceOf(parent)
		if err != nil || parentDev != dev {
			return path
		}
		path = parent
	}
}

func parseProcStat(b []byte) *HostCPU {
	var cpu *HostCPU
	var count uint64
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			if _, err := strconv.Atoi(fields[0][3:]); err == nil {
				count++
			}
			continue
		}
		v := make([]uint64, 8)
		for i := range v {
			if i+1 >= len(fields) {
				break
			}
			v[i], _ = strconv.ParseUint(fields[i+1], 10, 64)
		}
		user, nice, system, iowait, irq, softirq, steal := v[0], v[1], v[2], v[4], v[5], v[6], v[7]
		cpu = &HostCPU{
			UsageUsec:  ticksToUsec(user + nice + system + irq + softirq + steal),
			UserUsec:   ticksToUsec(user + nice),
			SystemUsec: ticksToUsec(system + irq + softirq),
			IowaitUsec: ticksToUsec(iowait),
			StealUsec:  ticksToUsec(steal),
		}
	}
	if cpu != nil {
		cpu.Count = count
	}
	return cpu
}

func ticksToUsec(ticks uint64) uint64 {
	return ticks * (1_000_000 / userHZ)
}

func parseLoadAvg(b []byte) *LoadAvg {
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return nil
	}
	var l LoadAvg
	var err1, err2, err3 error
	l.Load1, err1 = strconv.ParseFloat(fields[0], 64)
	l.Load5, err2 = strconv.ParseFloat(fields[1], 64)
	l.Load15, err3 = strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	return &l
}

func parseMeminfo(b []byte) *HostMemory {
	var m HostMemory
	var buffers, cached uint64
	found := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			v *= 1024
		}
		switch key {
		case "MemTotal":
			m.Total = v
			found = true
		case "MemAvailable":
			m.Available = v
		case "MemFree":
			m.Free = v
		case "Buffers":
			buffers = v
		case "Cached":
			cached = v
		case "SwapTotal":
			m.SwapTotal = v
		case "SwapFree":
			m.SwapFree = v
		}
	}
	if !found {
		return nil
	}
	m.Cached = buffers + cached
	return &m
}

func parseHostNetDev(b []byte, include func(name string) bool) *HostNet {
	m, n := sumNetDev(b, include)
	if n == 0 {
		return nil
	}
	return &HostNet{RxBytes: m.RxBytes, RxPackets: m.RxPackets, RxDropped: m.RxDropped, TxBytes: m.TxBytes, TxPackets: m.TxPackets, TxDropped: m.TxDropped}
}

func parseDiskstats(b []byte, include func(name string) bool) *IOStats {
	var s IOStats
	devices := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || !include(fields[2]) {
			continue
		}
		readOps, err1 := strconv.ParseUint(fields[3], 10, 64)
		readSectors, err2 := strconv.ParseUint(fields[5], 10, 64)
		writeOps, err3 := strconv.ParseUint(fields[7], 10, 64)
		writeSectors, err4 := strconv.ParseUint(fields[9], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		devices++
		s.ReadOps += readOps
		s.ReadBytes += readSectors * diskstatsSectorBytes
		s.WriteOps += writeOps
		s.WriteBytes += writeSectors * diskstatsSectorBytes
	}
	if devices == 0 {
		return nil
	}
	return &s
}

const diskstatsSectorBytes = 512
