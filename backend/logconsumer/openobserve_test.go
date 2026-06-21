package logconsumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestOpenObserveEndpoint(t *testing.T) {
	got, err := openObserveEndpoint("https://logs.example.com/root/", "prod")
	if err != nil {
		t.Fatalf("openObserveEndpoint: %v", err)
	}
	want := "https://logs.example.com/root/api/default/prod/_json"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestOpenObserveSpoolDir(t *testing.T) {
	got, err := openObserveSpoolDir("/var/lib/opendeploy-run-logs/12/4/3")
	if err != nil {
		t.Fatalf("openObserveSpoolDir: %v", err)
	}
	want := "/var/lib/opendeploy-run-logs/12/backpressure-area"
	if got != want {
		t.Fatalf("spool dir = %q, want %q", got, want)
	}
}

func TestOpenObserveConfigPath(t *testing.T) {
	got, err := OpenObserveConfigPath("/var/lib/opendeploy-run-logs/12/4/3")
	if err != nil {
		t.Fatalf("OpenObserveConfigPath: %v", err)
	}
	want := "/var/lib/opendeploy-run-logs/12/openobserve-config.json"
	if got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
}

func TestWriteAndLoadOpenObserveConfig(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "10", "2", "1")
	path, err := WriteOpenObserveConfig(basePath, "https://logs.example.com", "default", "token", "api", 3)
	if err != nil {
		t.Fatalf("WriteOpenObserveConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %v, want 0600", got)
	}
	cfg, err := LoadOpenObserveConfig(path)
	if err != nil {
		t.Fatalf("LoadOpenObserveConfig: %v", err)
	}
	if cfg.BasePath != basePath || cfg.URL != "https://logs.example.com" || cfg.Stream != "default" || cfg.IngestionToken != "token" || cfg.Svc != "api" || cfg.Version != 3 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestOpenObserveRecordWrapsPlainLine(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 11, 12, 13, time.UTC)
	formatter, err := newOpenObserveRecordFormatter("api", 3)
	if err != nil {
		t.Fatalf("newOpenObserveRecordFormatter: %v", err)
	}
	record, err := formatter.record(now, "stdout", []byte("hello"))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(record, &got); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if got["message"] != "hello" || got["stream"] != "stdout" || got["_timestamp"] != now.Format(time.RFC3339Nano) || got["svc"] != "api" || got["version"] != float64(3) {
		t.Fatalf("record = %#v", got)
	}
}

func TestOpenObserveRecordInjectsFieldsIntoValidJSONLine(t *testing.T) {
	formatter, err := newOpenObserveRecordFormatter("api", 3)
	if err != nil {
		t.Fatalf("newOpenObserveRecordFormatter: %v", err)
	}
	record, err := formatter.record(time.Now(), "stdout", []byte(`{"msg":"ok"}`))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	want := `{"msg":"ok","svc":"api","version":3}`
	if string(record) != want {
		t.Fatalf("record = %s, want %s", record, want)
	}
}

func TestOpenObserveRecordInjectsFieldsIntoEmptyJSONObject(t *testing.T) {
	formatter, err := newOpenObserveRecordFormatter("api", 3)
	if err != nil {
		t.Fatalf("newOpenObserveRecordFormatter: %v", err)
	}
	record, err := formatter.record(time.Now(), "stdout", []byte(`{}`))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	want := `{"svc":"api","version":3}`
	if string(record) != want {
		t.Fatalf("record = %s, want %s", record, want)
	}
}

func TestOpenObserveSpoolDrainSkipsLockedFile(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "10", "2", "1")
	sink, err := newOpenObserveSink(basePath, "https://logs.example.com", "default", "token")
	if err != nil {
		t.Fatalf("newOpenObserveSink: %v", err)
	}
	path := filepath.Join(sink.spoolDir, "openobserve.1.2.000001.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		t.Fatalf("create spool: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"message":"held"}` + "\n"); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("lock spool: %v", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if err := sink.drainSpool(context.Background()); err != nil {
		t.Fatalf("drainSpool: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("locked spool file was removed: %v", err)
	}
}

func TestOpenObserveSpoolDrainPostsAndRemoves(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/default/default/_json" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Basic token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	basePath := filepath.Join(t.TempDir(), "10", "2", "1")
	sink, err := newOpenObserveSink(basePath, server.URL, "default", "token")
	if err != nil {
		t.Fatalf("newOpenObserveSink: %v", err)
	}
	batch := [][]byte{[]byte(`{"message":"one"}`), []byte(`{"message":"two"}`)}
	if err := sink.spoolBatch(batch); err != nil {
		t.Fatalf("spoolBatch: %v", err)
	}
	if err := sink.drainSpool(context.Background()); err != nil {
		t.Fatalf("drainSpool: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	entries, err := os.ReadDir(sink.spoolDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool entries remaining = %d, want 0", len(entries))
	}
}
