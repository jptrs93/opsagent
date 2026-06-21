package logconsumer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const OpenObserveCommandName = "openobserve-log-consumer"

const openObserveBatchSize = 1_000
const openObserveFlushInterval = time.Second
const openObserveShutdownFlushTimeout = 200 * time.Millisecond
const openObserveHTTPTimeout = 10 * time.Second

func RunOpenObserveProcess(args []string) error {
	if len(args) != 3 || args[1] != OpenObserveCommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <config-path>", args[0], OpenObserveCommandName)
	}
	cfg, err := LoadOpenObserveConfig(args[2])
	if err != nil {
		return err
	}
	runOpenObserveLogger(cfg.BasePath, cfg.URL, cfg.Stream, cfg.IngestionToken, cfg.SAEmail, cfg.Svc, cfg.Version)
	return nil
}

type OpenObserveConfig struct {
	BasePath       string `json:"base_path"`
	URL            string `json:"url"`
	Stream         string `json:"stream"`
	IngestionToken string `json:"ingestion_token"`
	SAEmail        string `json:"sa_email"`
	Svc            string `json:"svc"`
	Version        int    `json:"version"`
}

func WriteOpenObserveConfig(basePath, openObserveURL, stream, token, saEmail, svc string, version int) (string, error) {
	path, err := OpenObserveConfigPath(basePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	b, err := json.Marshal(OpenObserveConfig{
		BasePath:       basePath,
		URL:            openObserveURL,
		Stream:         stream,
		IngestionToken: token,
		SAEmail:        saEmail,
		Svc:            svc,
		Version:        version,
	})
	if err != nil {
		return "", err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

func LoadOpenObserveConfig(path string) (OpenObserveConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return OpenObserveConfig{}, fmt.Errorf("read openobserve log consumer config: %w", err)
	}
	var cfg OpenObserveConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return OpenObserveConfig{}, fmt.Errorf("parse openobserve log consumer config: %w", err)
	}
	if cfg.BasePath == "" || cfg.URL == "" || cfg.Stream == "" || cfg.IngestionToken == "" || cfg.SAEmail == "" || cfg.Svc == "" || cfg.Version == 0 {
		return OpenObserveConfig{}, fmt.Errorf("openobserve log consumer config requires base_path, url, stream, ingestion_token, sa_email, svc, and version")
	}
	return cfg, nil
}

func runOpenObserveLogger(basePath, openObserveURL, stream, token, saEmail, svc string, version int) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		sink, err := newOpenObserveSink(basePath, openObserveURL, stream, token, saEmail)
		if err != nil {
			return err
		}
		formatter, err := newOpenObserveRecordFormatter(svc, version)
		if err != nil {
			return err
		}

		outlines := make(chan []byte, logOutputQueueSize)
		var wg sync.WaitGroup
		var stdoutErr error
		var stderrErr error
		closeInputs := sync.OnceFunc(func() {
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
		})

		wg.Go(func() {
			stdoutErr = processOpenObserveLinesWithClock(cfg.Stdout, "stdout", outlines, time.Now, formatter)
		})
		wg.Go(func() {
			stderrErr = processOpenObserveLinesWithClock(cfg.Stderr, "stderr", outlines, time.Now, formatter)
		})

		go func() {
			<-ctx.Done()
			closeInputs()
		}()

		go func() {
			wg.Wait()
			close(outlines)
		}()

		if err := ready(); err != nil {
			closeInputs()
			for range outlines {
			} // drain to allow line consumers to continue
			return err
		}

		batch := make([][]byte, 0, openObserveBatchSize)
		flush := func(ctx context.Context) bool {
			if len(batch) == 0 {
				return true
			}
			b := batch
			batch = make([][]byte, 0, openObserveBatchSize)
			if err := sink.sendBatch(ctx, b); err != nil {
				_ = sink.spoolBatch(b)
				return false
			}
			return true
		}

		ticker := time.NewTicker(openObserveFlushInterval)
		defer ticker.Stop()

		for outlines != nil {
			select {
			case line, ok := <-outlines:
				if !ok {
					outlines = nil
					continue
				}
				batch = append(batch, line)
				if len(batch) >= openObserveBatchSize {
					flush(context.Background())
				}
			case <-ticker.C:
				flush(context.Background())
				_ = sink.drainSpool(context.Background())
			}
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), openObserveShutdownFlushTimeout)
		flush(shutdownCtx)
		cancel()

		if ctx.Err() != nil && stdoutErr == nil && stderrErr == nil {
			return nil
		}
		var errs []error
		if stdoutErr != nil && ctx.Err() == nil {
			errs = append(errs, stdoutErr)
		}
		if stderrErr != nil && ctx.Err() == nil {
			errs = append(errs, stderrErr)
		}
		return errors.Join(errs...)
	})
}

func processOpenObserveLinesWithClock(r io.Reader, stream string, ch chan<- []byte, now func() time.Time, formatter openObserveRecordFormatter) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxUnformattedBlockBytes)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		record, err := formatter.record(now(), stream, line)
		if err != nil {
			return err
		}
		ch <- record
	}
	return scanner.Err()
}

type openObserveRecordFormatter struct {
	svc              string
	version          int
	objectSuffix     []byte
	emptyObjectBytes []byte
}

func newOpenObserveRecordFormatter(svc string, version int) (openObserveRecordFormatter, error) {
	fields, err := json.Marshal(struct {
		Svc     string `json:"svc"`
		Version int    `json:"version"`
	}{Svc: svc, Version: version})
	if err != nil {
		return openObserveRecordFormatter{}, err
	}
	return openObserveRecordFormatter{
		svc:              svc,
		version:          version,
		objectSuffix:     append([]byte{','}, fields[1:]...),
		emptyObjectBytes: fields,
	}, nil
}

func (f openObserveRecordFormatter) record(t time.Time, stream string, line []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(line)
	if json.Valid(trimmed) && len(trimmed) > 0 && trimmed[len(trimmed)-1] == '}' {
		if len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) == 0 {
			return bytes.Clone(f.emptyObjectBytes), nil
		}
		record := make([]byte, 0, len(trimmed)-1+len(f.objectSuffix))
		record = append(record, trimmed[:len(trimmed)-1]...)
		record = append(record, f.objectSuffix...)
		return record, nil
	}
	return f.wrapPlainLine(t, stream, line)
}

func (f openObserveRecordFormatter) wrapPlainLine(t time.Time, stream string, line []byte) ([]byte, error) {
	record := map[string]any{
		"_timestamp": t.UTC().Format(time.RFC3339Nano),
		"stream":     stream,
		"svc":        f.svc,
		"version":    f.version,
	}
	record["message"] = string(line)
	return json.Marshal(record)
}

type openObserveSink struct {
	endpoint string
	token    string
	saEmail  string
	client   *http.Client
	spoolDir string
	pid      int
	seq      uint64
}

func newOpenObserveSink(basePath, openObserveURL, stream, token, saEmail string) (*openObserveSink, error) {
	endpoint, err := openObserveEndpoint(openObserveURL, stream)
	if err != nil {
		return nil, err
	}
	spoolDir, err := openObserveSpoolDir(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(spoolDir, 0o750); err != nil {
		return nil, err
	}
	return &openObserveSink{
		endpoint: endpoint,
		token:    token,
		saEmail:  saEmail,
		client:   &http.Client{Timeout: openObserveHTTPTimeout},
		spoolDir: spoolDir,
		pid:      os.Getpid(),
	}, nil
}

func openObserveEndpoint(base, stream string) (string, error) {
	base = strings.TrimSpace(base)
	stream = strings.TrimSpace(stream)
	if base == "" {
		return "", fmt.Errorf("openobserve url is required")
	}
	if stream == "" {
		return "", fmt.Errorf("openobserve stream is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("openobserve url must include scheme and host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/default/" + url.PathEscape(stream) + "/_json"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func openObserveSpoolDir(basePath string) (string, error) {
	deploymentDir, err := openObserveDeploymentDir(basePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(deploymentDir, "backpressure-area"), nil
}

func OpenObserveConfigPath(basePath string) (string, error) {
	deploymentDir, err := openObserveDeploymentDir(basePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(deploymentDir, "openobserve-config.json"), nil
}

func openObserveDeploymentDir(basePath string) (string, error) {
	basePath = filepath.Clean(basePath)
	versionDir := filepath.Dir(basePath)
	deploymentDir := filepath.Dir(versionDir)
	if deploymentDir == "." || deploymentDir == string(filepath.Separator) {
		return "", fmt.Errorf("cannot derive openobserve deployment dir from %q", basePath)
	}
	return deploymentDir, nil
}

func (s *openObserveSink) sendBatch(ctx context.Context, batch [][]byte) error {
	if len(batch) == 0 {
		return nil
	}
	var body bytes.Buffer
	body.WriteByte('[')
	for i, line := range batch {
		if i > 0 {
			body.WriteByte(',')
		}
		body.Write(line)
	}
	body.WriteByte(']')

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", openObserveAuthorization(s.saEmail, s.token))
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("openobserve ingest returned %s", res.Status)
	}
	return nil
}

func openObserveAuthorization(saEmail, token string) string {
	token = strings.TrimSpace(token)
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "basic ") || strings.HasPrefix(lower, "bearer ") {
		return token
	}
	saEmail = strings.TrimSpace(saEmail)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(saEmail+":"+token))
}

func (s *openObserveSink) spoolBatch(batch [][]byte) error {
	if len(batch) == 0 {
		return nil
	}
	s.seq++
	name := fmt.Sprintf("openobserve.%d.%d.%06d.jsonl", time.Now().UTC().UnixNano(), s.pid, s.seq)
	tmpPath := filepath.Join(s.spoolDir, "."+name+".tmp")
	readyPath := filepath.Join(s.spoolDir, name)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	writeErr := writeSpoolBatch(f, batch)
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if writeErr != nil || unlockErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return errors.Join(writeErr, unlockErr, closeErr)
	}
	return os.Rename(tmpPath, readyPath)
}

func writeSpoolBatch(w io.Writer, batch [][]byte) error {
	for _, line := range batch {
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

func (s *openObserveSink) drainSpool(ctx context.Context) error {
	entries, err := os.ReadDir(s.spoolDir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, "openobserve.") && strings.HasSuffix(name, ".jsonl") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.drainSpoolFile(ctx, filepath.Join(s.spoolDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *openObserveSink) drainSpoolFile(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	batch := make([][]byte, 0, openObserveBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		b := batch
		batch = make([][]byte, 0, openObserveBatchSize)
		return s.sendBatch(ctx, b)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxUnformattedBlockBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		batch = append(batch, bytes.Clone(line))
		if len(batch) >= openObserveBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	return os.Remove(path)
}
