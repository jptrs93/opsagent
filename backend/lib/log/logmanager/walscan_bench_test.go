package logmanager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// Live-WAL scan benchmarks over real WAL files copied from a production node.
//
// The dataset root mirrors the node's on-disk layout: <root>/run-logs/<dep>/
// holds that deployment's bucket WAL files exactly as they sat under
// /var/lib/opendeploy-run-logs/<dep>/ (one 30-minute bucket per file, 0640,
// possibly with a torn tail on the newest bucket). The default root is
// test-logs/live-20260904 at the repo top level (gitignored); override it with
// LOGWAL_BENCH_ROOT and the deployment with LOGWAL_BENCH_DEPLOYMENT.
//
//	cd backend && go test ./lib/log/logmanager -run '^$' -bench BenchmarkLiveWAL -benchmem -benchtime 3x
//
// Setup copies the files into a fresh temp dir and points
// ainit.StaticConfig.LogWALDir at it, so the reader lists, opens and seeks the
// same way it does on the node. The copy is excluded from the timing.
const (
	liveWALBenchRoot       = "../../../../test-logs/live-20260904"
	liveWALBenchDeployment = int32(24)
)

// liveWALBenchEnv stages <root>/run-logs/<dep>/*.wal into a temp LogWALDir and
// returns the deployment id and the total bytes staged.
func liveWALBenchEnv(b *testing.B) (int32, int64) {
	b.Helper()
	root := liveWALBenchRoot
	if v := os.Getenv("LOGWAL_BENCH_ROOT"); v != "" {
		root = v
	}
	dep := liveWALBenchDeployment
	if v := os.Getenv("LOGWAL_BENCH_DEPLOYMENT"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &dep); err != nil {
			b.Fatalf("LOGWAL_BENCH_DEPLOYMENT=%q: %v", v, err)
		}
	}
	src := filepath.Join(root, "run-logs", fmt.Sprint(dep))
	entries, err := os.ReadDir(src)
	if err != nil {
		b.Skipf("live WAL dataset not found at %s: %v", src, err)
	}

	old := ainit.StaticConfig.LogWALDir
	ainit.StaticConfig.LogWALDir = filepath.Join(b.TempDir(), "run-logs")
	b.Cleanup(func() { ainit.StaticConfig.LogWALDir = old })
	dst := walDeploymentDir(dep)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		b.Fatal(err)
	}

	var total int64
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), walExt) {
			continue
		}
		n, err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
		if err != nil {
			b.Fatal(err)
		}
		total += n
		files++
	}
	if files == 0 {
		b.Skipf("no %s files under %s", walExt, src)
	}
	b.Logf("staged %d WAL files, %d bytes, deployment %d", files, total, dep)
	return dep, total
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// sealedProducer matches what the query path passes to streamRecords: the
// producer is treated as gone, so the reader drains what is on disk and
// returns instead of tailing.
func sealedProducer() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// BenchmarkLiveWALStream measures the raw record iterator over the deployment's
// WAL directory from a zero marker: file listing, sequential reads, framing,
// CRC and header decode, and one line copy per record. This is the floor for
// the "live wal" segment of a query trace.
func BenchmarkLiveWALStream(b *testing.B) {
	dep, total := liveWALBenchEnv(b)
	b.SetBytes(total)
	b.ResetTimer()
	var rows int64
	for range b.N {
		rows = 0
		for r, err := range streamRecords(sealedProducer(), dep, StreamMarker{}, false) {
			if err != nil {
				b.Fatal(err)
			}
			rows++
			_ = r
		}
	}
	reportRows(b, rows)
}

// BenchmarkLiveWALStreamParsed adds the per-record work a level-filtered query
// does on top of the iterator: build a visitRec on the scan's shared scanner
// and resolve its level. The gap to BenchmarkLiveWALStream is the level cost.
func BenchmarkLiveWALStreamParsed(b *testing.B) {
	dep, total := liveWALBenchEnv(b)
	b.SetBytes(total)
	b.ResetTimer()
	var rows, levelled int64
	for range b.N {
		rows, levelled = 0, 0
		sc := &lineScanner{}
		for r, err := range streamRecords(sealedProducer(), dep, StreamMarker{}, false) {
			if err != nil {
				b.Fatal(err)
			}
			rows++
			v := visitRec{rec: r.record, sc: sc}
			if v.levelValue() != "" {
				levelled++
			}
		}
	}
	reportRows(b, rows)
	b.ReportMetric(float64(levelled), "levelled/op")
}

// BenchmarkLiveWALStreamParseLine is the same walk through parseLine alone,
// kept as the reference the view is measured against.
func BenchmarkLiveWALStreamParseLine(b *testing.B) {
	dep, total := liveWALBenchEnv(b)
	b.SetBytes(total)
	b.ResetTimer()
	var rows, levelled int64
	for range b.N {
		rows, levelled = 0, 0
		for r, err := range streamRecords(sealedProducer(), dep, StreamMarker{}, false) {
			if err != nil {
				b.Fatal(err)
			}
			rows++
			if lvl, _, _ := parseLine(r.record.Line); lvl != "" {
				levelled++
			}
		}
	}
	reportRows(b, rows)
	b.ReportMetric(float64(levelled), "levelled/op")
}

// BenchmarkLiveWALStreamSorted wraps the iterator in the per-bucket key sort
// used by delimited range reads.
func BenchmarkLiveWALStreamSorted(b *testing.B) {
	dep, total := liveWALBenchEnv(b)
	b.SetBytes(total)
	b.ResetTimer()
	var rows int64
	for range b.N {
		rows = 0
		for r, err := range sortedByTime(streamRecords(sealedProducer(), dep, StreamMarker{}, false)) {
			if err != nil {
				b.Fatal(err)
			}
			rows++
			_ = r
		}
	}
	reportRows(b, rows)
}

func reportRows(b *testing.B, rows int64) {
	b.Helper()
	b.ReportMetric(float64(rows), "rows/op")
	if rows > 0 && b.N > 0 {
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(rows), "ns/row")
	}
}
