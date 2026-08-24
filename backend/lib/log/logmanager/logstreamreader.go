package logmanager

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jptrs93/goutil/contextu"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const streamBufLen = 128 * 1024

type LogSourceRef struct {
	day       int32
	bucket    int32
	filePath  string
	bucketEnd time.Time
}

// producerCtx is cancelled when the writer for this deployment has stopped and
// no further bytes will ever appear. It means drain and finish, not abort: the
// readers below still consume everything already on disk before returning.
func StreamDeploymentLogFiles(producerCtx context.Context, deploymentID int32, m StreamMarker) iter.Seq2[LogSourceRef, error] {
	// returns the exact file of m first or the first file of the stream if m is zero value. errors if expected file doesn't exist
	return func(yield func(LogSourceRef, error) bool) {
		q, err := listExistingFrom(deploymentID, m.day, m.bucket)
		if err != nil {
			yield(LogSourceRef{}, err)
			return
		}
		last := LogSourceRef{day: m.day, bucket: m.bucket}
		for {
			for _, last = range q {
				if !yield(last, nil) {
					return
				}
			}
			// read before listing: if the producer was already gone, the listing
			// that follows cannot miss a file it went on to write
			alive := producerCtx.Err() == nil
			q, err = listExistingFrom(deploymentID, last.day, last.bucket)
			if err != nil {
				yield(LogSourceRef{}, err)
				return
			}
			if len(q) > 0 && q[0] == last {
				q = q[1:]
			}
			if len(q) > 0 {
				continue
			}
			if !alive {
				return
			}
			// todo: optimise this - sleep longer in middle of buckets and shorter near expected boundary of new files
			contextu.Sleep(producerCtx, fileListInterval)
		}
	}
}

// StreamDeploymentLogRecords
// - Streams a deployment log records in order from a given marker.
// - The underlying log wal files are opened sequentially in the correct order transparently.
// - Resumes after m: m is the last record already consumed, so it is not re-emitted.
// - A zero m starts from the earliest available file and record
// - Whist the producerCtx is not cancelled, the stream blocks waiting for new records to be written.
func StreamDeploymentLogRecords(producerCtx context.Context, deploymentID int32, m StreamMarker) iter.Seq2[WrappedRecord, error] {
	return streamRecords(producerCtx, deploymentID, m, true)
}

// StreamDeploymentLogRecordsRange same as StreamDeploymentLogRecords but with an end range.
func StreamDeploymentLogRecordsRange(deploymentID int32, sIncl, eIncl StreamMarker) iter.Seq2[WrappedRecord, error] {
	return func(yield func(WrappedRecord, error) bool) {
		sealed, _ := newProducerCtx(context.Background(), false)
		for r, err := range streamRecords(sealed, deploymentID, sIncl, false) {
			if err != nil {
				yield(WrappedRecord{}, err)
				return
			}
			if eIncl.before(r.m) {
				return
			}
			if !yield(r, nil) {
				return
			}
			if !r.m.before(eIncl) {
				return
			}
		}
	}
}

var sortBufBytesThresh = int64(8 << 20)

// sortedByTime must only wrap delimited range reads: it reorders records, and
// the live tail's consumers (spool ranges, commit markers) require WAL byte
// order. Records of one bucket are buffered and stable-sorted by the record
// key, which yields a fully key-sorted stream because a record can only live
// in the bucket its timestamp belongs to. If a single bucket exceeds
// sortBufBytesThresh the sorted bottom half is yielded early, degrading to a
// sliding-window approximate sort for that bucket.
func sortedByTime(seq iter.Seq2[WrappedRecord, error]) iter.Seq2[WrappedRecord, error] {
	return func(yield func(WrappedRecord, error) bool) {
		var buf []WrappedRecord
		var bufBytes int64
		sorted := true
		flush := func(n int) bool {
			if n == 0 {
				return true
			}
			if !sorted {
				slices.SortStableFunc(buf, func(a, b WrappedRecord) int {
					return cmpRecordKey(&a.record, &b.record)
				})
				sorted = true
			}
			for _, r := range buf[:n] {
				if !yield(r, nil) {
					return false
				}
				bufBytes -= r.size
			}
			buf = append(buf[:0], buf[n:]...)
			return true
		}
		for r, err := range seq {
			if err != nil {
				yield(WrappedRecord{}, err)
				return
			}
			if len(buf) > 0 && (r.m.day != buf[0].m.day || r.m.bucket != buf[0].m.bucket) {
				if !flush(len(buf)) {
					return
				}
			}
			if len(buf) > 0 && cmpRecordKey(&r.record, &buf[len(buf)-1].record) < 0 {
				sorted = false
			}
			buf = append(buf, r)
			bufBytes += r.size
			if bufBytes > sortBufBytesThresh {
				if !flush(len(buf) / 2) {
					return
				}
			}
		}
		flush(len(buf))
	}
}

func streamRecords(producerCtx context.Context, deploymentID int32, m StreamMarker, skipMarkerRecord bool) iter.Seq2[WrappedRecord, error] {
	return func(yield func(WrappedRecord, error) bool) {
		offset := m.byteOffset
		skipThrough := int64(-1)
		if !m.isZero() && skipMarkerRecord {
			skipThrough = m.byteOffset
		}
		first := true
		for ls, err := range StreamDeploymentLogFiles(producerCtx, deploymentID, m) {
			if err != nil {
				yield(WrappedRecord{}, err)
				return
			}
			if first {
				first = false
				// the marker's own bucket may have been deleted as fully consumed;
				// the resume offset only applies when the first file is that bucket
				if ls.day != m.day || ls.bucket != m.bucket {
					offset = 0
					skipThrough = -1
				}
			}
			f, err := openAndSeek(ls.filePath, offset)
			if err != nil {
				yield(WrappedRecord{}, err)
				return
			}
			buf := make([]byte, streamBufLen)
			start := 0
			end := 0
			for {
				if start > streamBufLen-logv2.RecordMaxLen {
					end = copy(buf, buf[start:end])
					start = 0
				}
				alive := producerCtx.Err() == nil
				n, rerr := f.Read(buf[end:])
				if n > 0 {
					end += n
					for end-start >= logv2.RecordMinLen {
						rec, size, status := parseWalRecord(buf[start:end])
						if status == parseIncomplete {
							break
						}
						if status == parseInvalid {
							// locate the next candidate magic
							i := nextMagicIndex(buf[start+1 : end])
							if i < 0 {
								offset += int64(end - start)
								start = 0
								end = 0
								break
							}
							start += 1 + i
							offset += int64(1 + i)
							continue
						}
						if offset > skipThrough {
							wr := WrappedRecord{
								m:      StreamMarker{day: ls.day, bucket: ls.bucket, byteOffset: offset, time: rec.Time},
								record: rec,
								size:   int64(size),
							}
							if !yield(wr, nil) {
								_ = f.Close()
								return
							}
						}
						start += size
						offset += int64(size)
					}
				}
				if rerr != nil && !errors.Is(rerr, io.EOF) {
					_ = f.Close()
					yield(WrappedRecord{}, rerr)
					return
				}
				if n > 0 {
					continue
				}
				if !alive || clock().After(ls.bucketEnd.Add(reorderGraceWindow)) {
					break
				}
				contextu.Sleep(producerCtx, tailPollInterval) // offset untouched; just wait for the file to grow
			}
			_ = f.Close()
			offset = 0
			skipThrough = -1
		}
	}
}

func openAndSeek(path string, offset int64) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func listExistingFrom(deploymentID int32, day int32, bucket int32) ([]LogSourceRef, error) {
	walDir := walDeploymentDir(deploymentID)
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	from := int64(day)*bucketsPerDay + int64(bucket)
	var out []LogSourceRef
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		pos, ok := parseBucketPos(entry.Name())
		if !ok || pos < from {
			continue
		}
		d, b := splitPos(pos)
		out = append(out, LogSourceRef{
			day:       d,
			bucket:    b,
			filePath:  filepath.Join(walDir, entry.Name()),
			bucketEnd: posTime(pos + 1),
		})
	}
	slices.SortFunc(out, func(a, b LogSourceRef) int {
		if a.day != b.day {
			return int(a.day - b.day)
		}
		return int(a.bucket - b.bucket)
	})
	return out, nil
}
