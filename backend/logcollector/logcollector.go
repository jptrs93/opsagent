package logcollector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/logconsumer"
	"github.com/jptrs93/opsagent/backend/storage"
)

const (
	cleanupMarkerName = ".split-log-migration-v2"
	collectInterval   = 2 * time.Second
)

func DeleteLegacyLogbinOnce() error {
	marker := filepath.Join(ainit.StaticConfig.RunOutputDir, cleanupMarkerName)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := filepath.WalkDir(ainit.StaticConfig.RunOutputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != ainit.StaticConfig.RunOutputDir && filepath.Base(path) == "0" && filepath.Dir(path) == ainit.StaticConfig.RunOutputDir {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".logbin" {
			return os.Remove(path)
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}

func RunAll(ctx context.Context, store storage.OperatorStore, machine string) {
	deps, ch, _ := store.MustFetchSnapshotAndSubscribe(machine)
	m := &manager{ctx: ctx, processors: map[int32]*processorHandle{}}
	for _, dep := range deps {
		m.update(dep)
	}
	for {
		select {
		case <-ctx.Done():
			m.stop()
			return
		case dep, ok := <-ch:
			if !ok {
				m.stop()
				return
			}
			m.update(dep)
		}
	}
}

type manager struct {
	ctx        context.Context
	mu         sync.Mutex
	processors map[int32]*processorHandle
}

type processorHandle struct {
	updates chan apigen.DeploymentWithStatus
	cancel  context.CancelFunc
}

func (m *manager) update(dep apigen.DeploymentWithStatus) {
	if dep.Config.ID == 0 {
		return
	}
	m.mu.Lock()
	h := m.processors[dep.Config.ID]
	if h == nil {
		ctx, cancel := context.WithCancel(m.ctx)
		h = &processorHandle{updates: make(chan apigen.DeploymentWithStatus, 8), cancel: cancel}
		m.processors[dep.Config.ID] = h
		go func(id int32) {
			runProcessor(ctx, id, h.updates)
			m.mu.Lock()
			delete(m.processors, id)
			m.mu.Unlock()
		}(dep.Config.ID)
	}
	m.mu.Unlock()

	select {
	case h.updates <- dep:
	default:
		<-h.updates
		h.updates <- dep
	}
}

func (m *manager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.processors {
		h.cancel()
	}
}

func runProcessor(ctx context.Context, deploymentID int32, updates <-chan apigen.DeploymentWithStatus) {
	root := filepath.Join(ainit.StaticConfig.RunOutputDir, strconv.Itoa(int(deploymentID)))
	p := &processor{deploymentID: deploymentID, root: root, processed: apigen.RunProcessedOutputDir(deploymentID)}
	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()

	var latest apigen.DeploymentWithStatus
	for {
		select {
		case <-ctx.Done():
			_ = p.processOnce(true)
			return
		case latest = <-updates:
		case <-ticker.C:
		}
		drain := shouldDrain(latest)
		if err := p.processOnce(drain); err != nil {
			slog.Error("processing deployment logs failed", "deploymentID", deploymentID, "err", err)
		}
		if drain && !p.hasSourceFiles() {
			return
		}
	}
}

func shouldDrain(dep apigen.DeploymentWithStatus) bool {
	if dep.Config.Deleted {
		return true
	}
	s := dep.Status.Runner.Status
	return s == apigen.RunningStatus_STOPPED || s == apigen.RunningStatus_NO_DEPLOYMENT
}

type processor struct {
	deploymentID int32
	root         string
	processed    string
}

type LogLine struct {
	Time    int64
	Version int32
	Run     int32
	Stream  int8
	Line    []byte
	Raw     []byte

	// signal is used to indicate to the consumer we have received nothing .
	Signal int32
}

type sourceFile struct {
	path   string
	marker sourceMarker
}

type sourceRun struct {
	version int32
	run     int32
}

type sourceMarker int

const (
	sourceMarkerNone sourceMarker = iota
	sourceMarkerRotate
	sourceMarkerEnd
)

func (p *processor) processOnce(drain bool) error {
	lastProcessed, err := checkAndRecoverLastLogLine(p.processed)
	if err != nil {
		return err
	}
	runs, err := p.sourceRuns()
	if err != nil {
		return err
	}
	var lines []LogLine
	for _, src := range runs {
		for line, err := range consumeGreaterThan(lastProcessed, p.deploymentID, src.version, src.run) {
			if err != nil {
				return err
			}
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		sort.SliceStable(lines, func(i, j int) bool { return compareLogLine(lines[i], lines[j]) < 0 })
		if err := p.writeLines(lines); err != nil {
			return err
		}
		lastProcessed = lines[len(lines)-1]
	}
	sources, err := p.sourceFiles()
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.marker != sourceMarkerNone && (drain || sourceFullyProcessed(src.path, lastProcessed)) {
			if err := os.Remove(src.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (p *processor) sourceRuns() ([]sourceRun, error) {
	sources, err := p.sourceFiles()
	if err != nil {
		return nil, err
	}
	seen := map[sourceRun]bool{}
	for _, src := range sources {
		run, err := parsePathInt32(filepath.Base(filepath.Dir(src.path)))
		if err != nil {
			continue
		}
		version, err := parsePathInt32(filepath.Base(filepath.Dir(filepath.Dir(src.path))))
		if err != nil {
			continue
		}
		seen[sourceRun{version: version, run: run}] = true
	}
	runs := make([]sourceRun, 0, len(seen))
	for run := range seen {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].version != runs[j].version {
			return runs[i].version < runs[j].version
		}
		return runs[i].run < runs[j].run
	})
	return runs, nil
}

func checkAndRecoverLastLogLine(processedDir string) (LogLine, error) {
	files, err := processedFiles(processedDir)
	if err != nil {
		return LogLine{}, err
	}
	for _, path := range files {
		line, ok, err := recoverLastLogLine(path)
		if err != nil {
			return LogLine{}, err
		}
		if ok {
			return line, nil
		}
	}
	return LogLine{}, nil
}

func processedFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".logbin" {
			continue
		}
		if _, err := time.ParseInLocation("20060102_15", strings.TrimSuffix(entry.Name(), ".logbin"), time.UTC); err != nil {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, nil
}

func recoverLastLogLine(path string) (LogLine, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LogLine{}, false, nil
		}
		return LogLine{}, false, err
	}
	for suffixStart := len(data) - logconsumer.SplitRecordTrailerLen; suffixStart >= 0; suffixStart -= 4 {
		line, ok := decodeRecordEndingAt(data, suffixStart)
		if !ok || line.Time == 0 {
			continue
		}
		return line, true, os.Truncate(path, int64(suffixStart+logconsumer.SplitRecordTrailerLen))
	}
	return LogLine{}, false, os.Truncate(path, 0)
}

func decodeRecordEndingAt(data []byte, suffixStart int) (LogLine, bool) {
	if suffixStart < logconsumer.SplitRecordHeaderLen-logconsumer.SplitRecordTrailerLen || suffixStart+4 > len(data) {
		return LogLine{}, false
	}
	length := int(binary.BigEndian.Uint32(data[suffixStart : suffixStart+4]))
	if length < logconsumer.SplitRecordPayloadLen {
		return LogLine{}, false
	}
	prefixStart := suffixStart - length - logconsumer.SplitRecordLengthLen
	if prefixStart < 0 {
		return LogLine{}, false
	}
	if int(binary.BigEndian.Uint32(data[prefixStart:prefixStart+4])) != length {
		return LogLine{}, false
	}
	line, err := decodeRecord(data[prefixStart : suffixStart+4])
	return line, err == nil
}

func (p *processor) sourceFiles() ([]sourceFile, error) {
	var files []sourceFile
	err := filepath.WalkDir(p.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != p.root && filepath.Base(path) == "processed" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSourceLogFile(filepath.Base(path)) {
			return nil
		}
		marker, err := sourceFileMarker(path)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{path: path, marker: marker})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func isSourceLogFile(name string) bool {
	if !strings.HasSuffix(name, ".logbin") {
		return false
	}
	base := strings.TrimSuffix(name, ".logbin")
	if strings.HasPrefix(base, "stdout") {
		_, err := strconv.Atoi(strings.TrimPrefix(base, "stdout"))
		return err == nil
	}
	if strings.HasPrefix(base, "stderr") {
		_, err := strconv.Atoi(strings.TrimPrefix(base, "stderr"))
		return err == nil
	}
	return false
}

func sourceFileMarker(path string) (sourceMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sourceMarkerNone, nil
		}
		return sourceMarkerNone, err
	}
	line, ok := decodeRecordEndingAt(data, len(data)-logconsumer.SplitRecordTrailerLen)
	if !ok || line.Time != 0 {
		return sourceMarkerNone, nil
	}
	if line.Stream == logconsumer.SplitMarkerRotate {
		return sourceMarkerRotate, nil
	}
	if line.Stream == logconsumer.SplitMarkerEnd {
		return sourceMarkerEnd, nil
	}
	return sourceMarkerNone, nil
}

func readRecord(r io.Reader) (LogLine, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return LogLine{}, io.EOF
		}
		return LogLine{}, err
	}
	length := int(binary.BigEndian.Uint32(prefix[:]))
	if length < logconsumer.SplitRecordPayloadLen {
		return LogLine{}, fmt.Errorf("invalid record length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return LogLine{}, err
	}
	var suffix [4]byte
	if _, err := io.ReadFull(r, suffix[:]); err != nil {
		return LogLine{}, err
	}
	if binary.BigEndian.Uint32(suffix[:]) != uint32(length) {
		return LogLine{}, fmt.Errorf("record length suffix mismatch")
	}
	raw := make([]byte, 4+len(payload)+4)
	copy(raw[:4], prefix[:])
	copy(raw[4:], payload)
	copy(raw[len(raw)-4:], suffix[:])
	return decodeRecord(raw)
}

func decodeRecord(raw []byte) (LogLine, error) {
	if len(raw) < logconsumer.SplitRecordMinLen {
		return LogLine{}, fmt.Errorf("record too short")
	}
	length := int(binary.BigEndian.Uint32(raw[:4]))
	if length < logconsumer.SplitRecordPayloadLen || len(raw) != length+8 {
		return LogLine{}, fmt.Errorf("invalid record length %d", length)
	}
	if binary.BigEndian.Uint32(raw[len(raw)-4:]) != uint32(length) {
		return LogLine{}, fmt.Errorf("record length suffix mismatch")
	}
	payload := raw[4 : len(raw)-4]
	return LogLine{
		Time:    int64(binary.BigEndian.Uint64(payload[:8])),
		Version: int32(binary.BigEndian.Uint32(payload[8:12])),
		Run:     int32(binary.BigEndian.Uint32(payload[12:16])),
		Stream:  int8(payload[16]),
		Line:    append([]byte(nil), payload[17:]...),
		Raw:     append([]byte(nil), raw...),
	}, nil
}

func compareLogLine(a, b LogLine) int {
	if a.Time != b.Time {
		if a.Time < b.Time {
			return -1
		}
		return 1
	}
	if a.Version != b.Version {
		return compareInt32(a.Version, b.Version)
	}
	if a.Run != b.Run {
		return compareInt32(a.Run, b.Run)
	}
	if a.Stream != b.Stream {
		if a.Stream < b.Stream {
			return -1
		}
		return 1
	}
	return bytes.Compare(a.Line, b.Line)
}

func compareInt32(a, b int32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (p *processor) writeLines(lines []LogLine) error {
	if err := os.MkdirAll(p.processed, 0o750); err != nil {
		return err
	}
	for _, line := range lines {
		rel := time.Unix(0, line.Time).UTC().Format("20060102_15") + ".logbin"
		path := filepath.Join(p.processed, rel)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		n, err := f.Write(line.Raw)
		if err == nil && n != len(line.Raw) {
			err = io.ErrShortWrite
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func sourceFullyProcessed(path string, lastProcessed LogLine) bool {
	f, err := os.Open(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	defer f.Close()
	for {
		line, err := readRecord(f)
		if err != nil {
			return errors.Is(err, io.EOF)
		}
		if line.Time == 0 {
			return true
		}
		if compareLogLine(line, lastProcessed) > 0 {
			return false
		}
	}
}

func (p *processor) hasSourceFiles() bool {
	files, err := p.sourceFiles()
	return err == nil && len(files) > 0
}
