package logmanager

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

func walEnv(t *testing.T) string {
	t.Helper()
	old := ainit.StaticConfig.LogWALDir
	ainit.StaticConfig.LogWALDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.LogWALDir = old })
	dir := walDeploymentDir(testDeploymentID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func streamTiming(t *testing.T, grace, poll, list time.Duration) {
	t.Helper()
	pg, pp, pl := reorderGraceWindow, tailPollInterval, fileListInterval
	reorderGraceWindow, tailPollInterval, fileListInterval = grace, poll, list
	t.Cleanup(func() { reorderGraceWindow, tailPollInterval, fileListInterval = pg, pp, pl })
}

func writeBucket(t *testing.T, dir, bucket string, chunks ...[]byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, bucket+walExt), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, chunk := range chunks {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
}

func writeBucketAfter(dir, bucket string, d time.Duration, chunks ...[]byte) {
	time.Sleep(d)
	f, err := os.OpenFile(filepath.Join(dir, bucket+walExt), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	for _, chunk := range chunks {
		if _, err := f.Write(chunk); err != nil {
			return
		}
	}
}

// deadProducer is the common case for reading sealed buckets: nothing more will
// be written, so the stream drains what exists and ends.
func deadProducer() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func drainStream(t *testing.T, ctx context.Context, deploymentID int32, m StreamMarker) []WrappedRecord {
	t.Helper()
	var got []WrappedRecord
	for r, err := range StreamDeploymentLogRecords(ctx, deploymentID, m) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r.owned())
	}
	return got
}

func openFDCount(t *testing.T) int {
	t.Helper()
	d, err := os.Open("/dev/fd")
	if err != nil {
		t.Skipf("cannot enumerate open descriptors: %v", err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		t.Skipf("cannot enumerate open descriptors: %v", err)
	}
	return len(names)
}

func TestStreamRecordsReadsBucketsInOrder(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStderr, "beta\n"),
	)
	writeBucket(t, dir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 2, 3, logv2.StreamStdout, "gamma\n"),
	)

	got := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if want := []string{"alpha\n", "beta\n", "gamma\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[0].m.byteOffset != 0 || got[1].m.byteOffset != got[0].size {
		t.Fatalf("offsets = %d, %d; want 0, %d", got[0].m.byteOffset, got[1].m.byteOffset, got[0].size)
	}
	if got[2].m.byteOffset != 0 {
		t.Fatalf("third offset = %d, want 0 (new bucket)", got[2].m.byteOffset)
	}
	if got[2].record.Deployment != testDeploymentID || got[2].record.Run != 3 {
		t.Fatalf("record identity not preserved: %#v", got[2].record)
	}
}

func TestStreamRecordsResumeSkipsCommittedRecord(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
		record(t, "2026-06-15T14:32:01Z", 1, 1, logv2.StreamStdout, "gamma\n"),
	)

	all := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	got := drainStream(t, deadProducer(), testDeploymentID, all[0].m)
	if want := []string{"beta\n", "gamma\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if last := drainStream(t, deadProducer(), testDeploymentID, all[2].m); len(last) != 0 {
		t.Fatalf("resuming from the final marker re-emitted %#v", lines(last))
	}
}

func TestStreamRecordsResumeOffsetAppliesOnlyToMarkerFile(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	writeBucket(t, dir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "gamma\n"),
		record(t, "2026-06-15T15:01:01Z", 1, 1, logv2.StreamStdout, "delta\n"),
	)

	all := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	got := drainStream(t, deadProducer(), testDeploymentID, all[0].m)
	if want := []string{"beta\n", "gamma\n", "delta\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[1].m.byteOffset != 0 {
		t.Fatalf("second file first record offset = %d, want 0", got[1].m.byteOffset)
	}
}

func TestStreamRecordsTailsThenDrainsOnProducerExit(t *testing.T) {
	streamTiming(t, time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	dir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, dir, bucket, record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		writeBucketAfter(dir, bucket, 150*time.Millisecond,
			record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"))
		cancel()
	}()

	done := make(chan []string, 1)
	go func() { done <- lines(drainStream(t, ctx, testDeploymentID, StreamMarker{})) }()
	select {
	case got := <-done:
		if want := []string{"alpha\n", "beta\n"}; !equalStrings(got, want) {
			t.Fatalf("lines = %#v, want %#v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not finish after the producer exited")
	}
}

func TestStreamRecordsHandlesMaxSizeLinesAcrossBuffer(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	// size the leading record so the following max-size record leaves the buffer
	// full with an incomplete window and start inside (streamBufLen-RecordMaxLen,
	// 64*1024] - the gap the old fixed 64KB compaction threshold missed
	framing := logv2.RecordMaxLen - logv2.MaxLineLen
	lead := bytes.Repeat([]byte("l"), streamBufLen-logv2.RecordMaxLen+6-framing)
	long := bytes.Repeat([]byte("x"), logv2.MaxLineLen)
	if n := len(lead) + framing; n <= streamBufLen-logv2.RecordMaxLen || n > 64*1024 {
		t.Fatalf("lead record of %d bytes does not sit in the missed window", n)
	}
	want := []string{string(lead), string(long), "tail\n"}
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, string(lead)),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, string(long)),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, "tail\n"),
	)

	done := make(chan []string, 1)
	go func() { done <- lines(drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})) }()
	select {
	case got := <-done:
		if !equalStrings(got, want) {
			t.Fatalf("got %d lines, want %d", len(got), len(want))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream stalled on records spanning the read buffer")
	}
}

func TestStreamRecordsClosesEveryBucketFile(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	for _, bucket := range []string{"20260615_1400", "20260615_1430", "20260615_1500", "20260615_1530"} {
		writeBucket(t, dir, bucket, record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"))
	}

	before := openFDCount(t)
	if got := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{}); len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	if after := openFDCount(t); after > before {
		t.Fatalf("open descriptors grew from %d to %d", before, after)
	}
}

func TestStreamRecordsSkipsForwardWhenMarkerBucketMissing(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1500", record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "alpha\n"))

	m := StreamMarker{day: 20618, bucket: 29, byteOffset: 999, time: 1}
	got := drainStream(t, deadProducer(), testDeploymentID, m)
	if want := []string{"alpha\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[0].m.byteOffset != 0 {
		t.Fatalf("offset = %d, want 0 (marker offset must not apply to a later bucket)", got[0].m.byteOffset)
	}
}

func TestStreamFilesZeroMarkerReadsFromStart(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1430", record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"))

	var got []LogSourceRef
	for ref, err := range StreamDeploymentLogFiles(deadProducer(), testDeploymentID, StreamMarker{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ref)
	}
	if len(got) != 1 {
		t.Fatalf("got %d files, want 1", len(got))
	}
}

func TestStreamFilesEmptyDirWithDeadProducer(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	walEnv(t)
	done := make(chan int, 1)
	go func() {
		n := 0
		for range StreamDeploymentLogFiles(deadProducer(), testDeploymentID, StreamMarker{}) {
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("yielded %d files from an empty dir", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("empty dir with a dead producer did not terminate")
	}
}

// shiftClock runs the package clock at real speed from base, so grace windows
// tied to wall-clock bucket boundaries can be exercised in milliseconds.
func shiftClock(t *testing.T, base string) {
	t.Helper()
	at := mustTime(t, base)
	started := time.Now()
	prev := clock
	clock = func() time.Time { return at.Add(time.Since(started)) }
	t.Cleanup(func() { clock = prev })
}

func TestStreamRecordsResyncsPastTornRecords(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	truncated := record(t, "2026-06-15T14:30:04Z", 1, 1, logv2.StreamStdout, "delta\n")
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		bytes.Repeat([]byte{0xff}, 37),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
		truncated[:len(truncated)/2],
	)

	got := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if want := []string{"alpha\n", "beta\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[1].m.byteOffset != got[0].size+37 {
		t.Fatalf("beta offset = %d, want %d", got[1].m.byteOffset, got[0].size+37)
	}
}

func TestStreamRecordsRejectsFalseMagicCandidates(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	line := append(bytes.Repeat([]byte{logv2.RecordMagic}, 64), []byte("alpha\n")...)
	writeBucket(t, dir, "20260615_1430",
		bytes.Repeat([]byte{logv2.RecordMagic}, 40),
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, string(line)),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)

	got := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if want := []string{string(line), "beta\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[0].m.byteOffset != 40 {
		t.Fatalf("first record offset = %d, want 40", got[0].m.byteOffset)
	}
}

func TestStreamRecordsKeepsOffsetsAcrossDiscardedBuffer(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	alpha := record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n")
	writeBucket(t, dir, "20260615_1430",
		alpha,
		bytes.Repeat([]byte{0x00}, streamBufLen-len(alpha)),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)

	got := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if want := []string{"alpha\n", "beta\n"}; !equalStrings(lines(got), want) {
		t.Fatalf("lines = %#v, want %#v", lines(got), want)
	}
	if got[1].m.byteOffset != streamBufLen {
		t.Fatalf("beta offset = %d, want %d", got[1].m.byteOffset, streamBufLen)
	}
}

func TestStreamRecordsRangeIsInclusiveBothEnds(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	writeBucket(t, dir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "gamma\n"),
		record(t, "2026-06-15T15:01:01Z", 1, 1, logv2.StreamStdout, "delta\n"),
	)
	all := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})
	if len(all) != 4 {
		t.Fatalf("len = %d, want 4", len(all))
	}

	collect := func(s, e StreamMarker) []string {
		var got []string
		for r, err := range StreamDeploymentLogRecordsRange(testDeploymentID, s, e) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, string(r.record.Line))
		}
		return got
	}
	if want := []string{"beta\n", "gamma\n"}; !equalStrings(collect(all[1].m, all[2].m), want) {
		t.Fatalf("cross bucket range = %#v, want %#v", collect(all[1].m, all[2].m), want)
	}
	if want := []string{"alpha\n"}; !equalStrings(collect(all[0].m, all[0].m), want) {
		t.Fatalf("single record range = %#v, want %#v", collect(all[0].m, all[0].m), want)
	}
	if want := []string{"alpha\n", "beta\n", "gamma\n", "delta\n"}; !equalStrings(collect(all[0].m, all[3].m), want) {
		t.Fatalf("full range = %#v, want %#v", collect(all[0].m, all[3].m), want)
	}
}

func TestStreamRecordsRangeStopsWithoutTailing(t *testing.T) {
	streamTiming(t, time.Hour, time.Hour, time.Hour)
	dir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, dir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	all := drainStream(t, deadProducer(), testDeploymentID, StreamMarker{})

	done := make(chan []string, 1)
	go func() {
		var got []string
		for r, err := range StreamDeploymentLogRecordsRange(testDeploymentID, all[0].m, all[0].m) {
			if err != nil {
				return
			}
			got = append(got, string(r.record.Line))
		}
		done <- got
	}()
	select {
	case got := <-done:
		if want := []string{"alpha\n"}; !equalStrings(got, want) {
			t.Fatalf("lines = %#v, want %#v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("range read tailed the live bucket instead of stopping at its end")
	}
}

func wrapped(day, bucket int32, timeNs int64, line string) WrappedRecord {
	return WrappedRecord{
		m:      StreamMarker{day: day, bucket: bucket, time: timeNs},
		record: apigen.RawLogLine{Time: timeNs, Line: []byte(line)},
		size:   int64(len(line)),
	}
}

func seqOf(recs []WrappedRecord, err error) iter.Seq2[WrappedRecord, error] {
	return func(yield func(WrappedRecord, error) bool) {
		for _, r := range recs {
			if !yield(r, nil) {
				return
			}
		}
		if err != nil {
			yield(WrappedRecord{}, err)
		}
	}
}

func drainSorted(t *testing.T, seq iter.Seq2[WrappedRecord, error]) []string {
	t.Helper()
	var got []string
	for r, err := range sortedByTime(seq) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(r.record.Line))
	}
	return got
}

func TestSortedByTimeSortsWithinEachBucket(t *testing.T) {
	got := drainSorted(t, seqOf([]WrappedRecord{
		wrapped(1, 0, 2, "b"),
		wrapped(1, 0, 1, "a"),
		wrapped(1, 0, 3, "c"),
		wrapped(1, 1, 5, "e"),
		wrapped(1, 1, 4, "d"),
	}, nil))
	if want := []string{"a", "b", "c", "d", "e"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestSortedByTimeStableOnEqualTimes(t *testing.T) {
	got := drainSorted(t, seqOf([]WrappedRecord{
		wrapped(1, 0, 5, "first"),
		wrapped(1, 0, 5, "second"),
		wrapped(1, 0, 3, "early"),
	}, nil))
	if want := []string{"early", "first", "second"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestSortedByTimeThresholdYieldsSlidingWindow(t *testing.T) {
	prev := sortBufBytesThresh
	sortBufBytesThresh = 10
	t.Cleanup(func() { sortBufBytesThresh = prev })
	got := drainSorted(t, seqOf([]WrappedRecord{
		wrapped(1, 0, 4, "line-4"),
		wrapped(1, 0, 3, "line-3"),
		wrapped(1, 0, 2, "line-2"),
		wrapped(1, 0, 1, "line-1"),
	}, nil))
	// each append overflows the 10-byte threshold, sorting and yielding half
	// the window: approximate order, every record emitted exactly once
	if want := []string{"line-3", "line-2", "line-1", "line-4"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestSortedByTimePropagatesErrorWithoutFlushing(t *testing.T) {
	boom := errors.New("boom")
	var got []string
	var gotErr error
	for r, err := range sortedByTime(seqOf([]WrappedRecord{
		wrapped(1, 0, 1, "a"),
		wrapped(1, 0, 2, "b"),
	}, boom)) {
		if err != nil {
			gotErr = err
			break
		}
		got = append(got, string(r.record.Line))
	}
	if !errors.Is(gotErr, boom) {
		t.Fatalf("err = %v, want %v", gotErr, boom)
	}
	if len(got) != 0 {
		t.Fatalf("yielded %#v before the error; buffered records must not flush", got)
	}
}

func TestStreamRecordsWaitsForStragglerAtDayBoundary(t *testing.T) {
	streamTiming(t, 600*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	// already past midnight, so only the reorder grace keeps the previous day's
	// bucket open for the straggler
	shiftClock(t, "2026-06-16T00:00:00.05Z")
	dir := walEnv(t)
	writeBucket(t, dir, "20260615_2330",
		record(t, "2026-06-15T23:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"))
	writeBucket(t, dir, "20260616_0000",
		record(t, "2026-06-16T00:00:01Z", 1, 1, logv2.StreamStdout, "nextday\n"))
	go writeBucketAfter(dir, "20260615_2330", 150*time.Millisecond,
		record(t, "2026-06-15T23:59:01Z", 1, 1, logv2.StreamStdout, "straggler\n"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []string, 1)
	go func() {
		var got []string
		for r, err := range StreamDeploymentLogRecords(ctx, testDeploymentID, StreamMarker{}) {
			if err != nil {
				t.Error(err)
				break
			}
			got = append(got, string(r.record.Line))
			if len(got) == 3 {
				break
			}
		}
		done <- got
	}()
	select {
	case got := <-done:
		if want := []string{"alpha\n", "straggler\n", "nextday\n"}; !equalStrings(got, want) {
			t.Fatalf("lines = %#v, want %#v", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("day boundary straggler was never picked up")
	}
}
