package metrics

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type CgroupMetrics struct {
	CPU            *CPUStats
	Memory         *MemoryStats
	IO             *IOStats
	Pids           *uint64
	CPUPressure    *Pressure
	MemoryPressure *Pressure
	IOPressure     *Pressure
}

type CPUStats struct {
	UsageUsec     uint64
	UserUsec      uint64
	SystemUsec    uint64
	NrThrottled   uint64
	ThrottledUsec uint64
}

type MemoryStats struct {
	Current uint64
	Peak    *uint64
	Anon    uint64
	File    uint64
	Kernel  uint64
	Shmem   uint64
	OOM     uint64
	OOMKill uint64
}

type IOStats struct {
	ReadBytes  uint64
	WriteBytes uint64
	ReadOps    uint64
	WriteOps   uint64
}

type Pressure struct {
	Some PressureLine
	Full PressureLine
}

type PressureLine struct {
	Avg10     float64
	Avg60     float64
	Avg300    float64
	TotalUsec uint64
}

func readCgroup(dir string) (CgroupMetrics, error) {
	var m CgroupMetrics
	var firstErr error
	read := func(name string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil, false
		}
		return b, true
	}

	if b, ok := read("cpu.stat"); ok {
		m.CPU = parseCPUStat(b)
	}
	if b, ok := read("memory.current"); ok {
		current, err := parseUint(b)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("memory.current: %w", err)
			}
		} else {
			m.Memory = &MemoryStats{Current: current}
			if b, err := os.ReadFile(filepath.Join(dir, "memory.peak")); err == nil {
				if peak, err := parseUint(b); err == nil {
					m.Memory.Peak = &peak
				}
			}
			if b, ok := read("memory.stat"); ok {
				parseMemoryStat(b, m.Memory)
			}
			if b, ok := read("memory.events"); ok {
				parseMemoryEvents(b, m.Memory)
			}
		}
	}
	if b, ok := read("io.stat"); ok {
		m.IO = parseIOStat(b)
	}
	if b, ok := read("pids.current"); ok {
		if pids, err := parseUint(b); err == nil {
			m.Pids = &pids
		} else if firstErr == nil {
			firstErr = fmt.Errorf("pids.current: %w", err)
		}
	}
	if b, ok := read("cpu.pressure"); ok {
		m.CPUPressure = parsePressure(b)
	}
	if b, ok := read("memory.pressure"); ok {
		m.MemoryPressure = parsePressure(b)
	}
	if b, ok := read("io.pressure"); ok {
		m.IOPressure = parsePressure(b)
	}
	return m, firstErr
}

func procCgroupPath(pid uint32) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, nil
		}
	}
	return "", errors.New("no cgroup v2 entry in /proc/<pid>/cgroup")
}

func parseUint(b []byte) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

func keyValues(b []byte, fn func(key, value string)) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), " ")
		if ok {
			fn(key, value)
		}
	}
}

func parseCPUStat(b []byte) *CPUStats {
	var s CPUStats
	keyValues(b, func(key, value string) {
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return
		}
		switch key {
		case "usage_usec":
			s.UsageUsec = v
		case "user_usec":
			s.UserUsec = v
		case "system_usec":
			s.SystemUsec = v
		case "nr_throttled":
			s.NrThrottled = v
		case "throttled_usec":
			s.ThrottledUsec = v
		}
	})
	return &s
}

func parseMemoryStat(b []byte, m *MemoryStats) {
	keyValues(b, func(key, value string) {
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return
		}
		switch key {
		case "anon":
			m.Anon = v
		case "file":
			m.File = v
		case "kernel":
			m.Kernel = v
		case "shmem":
			m.Shmem = v
		}
	})
}

func parseMemoryEvents(b []byte, m *MemoryStats) {
	keyValues(b, func(key, value string) {
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return
		}
		switch key {
		case "oom":
			m.OOM = v
		case "oom_kill":
			m.OOMKill = v
		}
	})
}

func parseIOStat(b []byte) *IOStats {
	var s IOStats
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			key, value, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			v, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				continue
			}
			switch key {
			case "rbytes":
				s.ReadBytes += v
			case "wbytes":
				s.WriteBytes += v
			case "rios":
				s.ReadOps += v
			case "wios":
				s.WriteOps += v
			}
		}
	}
	return &s
}

func parsePressure(b []byte) *Pressure {
	var p Pressure
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var line *PressureLine
		switch fields[0] {
		case "some":
			line = &p.Some
		case "full":
			line = &p.Full
		default:
			continue
		}
		for _, f := range fields[1:] {
			key, value, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch key {
			case "avg10":
				line.Avg10, _ = strconv.ParseFloat(value, 64)
			case "avg60":
				line.Avg60, _ = strconv.ParseFloat(value, 64)
			case "avg300":
				line.Avg300, _ = strconv.ParseFloat(value, 64)
			case "total":
				line.TotalUsec, _ = strconv.ParseUint(value, 10, 64)
			}
		}
	}
	return &p
}
