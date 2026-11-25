package metrics

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type NetMetrics struct {
	RxBytes   uint64
	RxPackets uint64
	RxDropped uint64
	TxBytes   uint64
	TxPackets uint64
	TxDropped uint64
	TCP       TCPStates
}

type TCPStates struct {
	Established uint64
	Listen      uint64
	TimeWait    uint64
	CloseWait   uint64
	Other       uint64
}

func readNet(pid uint32) (*NetMetrics, error) {
	proc := fmt.Sprintf("/proc/%d", pid)
	dev, err := os.ReadFile(proc + "/net/dev")
	if err != nil {
		return nil, err
	}
	m := parseNetDev(dev)
	var firstErr error
	for _, name := range []string{"/net/tcp", "/net/tcp6"} {
		b, err := os.ReadFile(proc + name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parseTCP(b, &m.TCP)
	}
	return m, firstErr
}

func readOpenFDs(pid uint32) *uint64 {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil
	}
	n := uint64(len(entries))
	return &n
}

func parseNetDev(b []byte) *NetMetrics {
	m, _ := sumNetDev(b, func(name string) bool { return name != "lo" })
	return &m
}

func sumNetDev(b []byte, include func(name string) bool) (NetMetrics, int) {
	var m NetMetrics
	interfaces := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if !include(strings.TrimSpace(name)) {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 12 {
			continue
		}
		v := make([]uint64, 12)
		for i := range v {
			n, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				continue
			}
			v[i] = n
		}
		interfaces++
		m.RxBytes += v[0]
		m.RxPackets += v[1]
		m.RxDropped += v[3]
		m.TxBytes += v[8]
		m.TxPackets += v[9]
		m.TxDropped += v[11]
	}
	return m, interfaces
}

func parseTCP(b []byte, s *TCPStates) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan()
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		switch fields[3] {
		case "01":
			s.Established++
		case "0A":
			s.Listen++
		case "06":
			s.TimeWait++
		case "08":
			s.CloseWait++
		default:
			s.Other++
		}
	}
}
