package logmanager

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	bucketDuration = 30 * time.Minute
	bucketLayout   = "20060102_1504"
	walExt         = ".wal"
	daySeconds     = 86400
	bucketSeconds  = int64(bucketDuration / time.Second)
	bucketsPerDay  = daySeconds / bucketSeconds
)

type StreamMarker struct {
	day        int32
	bucket     int32
	byteOffset int64
	time       int64
}

func (m StreamMarker) pos() int64 { return int64(m.day)*bucketsPerDay + int64(m.bucket) }

func (m StreamMarker) isZero() bool {
	return m.day == 0 && m.bucket == 0 && m.byteOffset == 0 && m.time == 0
}

func (m StreamMarker) before(o StreamMarker) bool {
	if p, q := m.pos(), o.pos(); p != q {
		return p < q
	}
	return m.byteOffset < o.byteOffset
}

type WrappedRecord struct {
	m      StreamMarker
	record apigen.RawLogLine
	size   int64
}

// Streams may return slightly out of order up to seconds range however they guarantee that records for different days will never be out of order.

func parseBucketPos(name string) (int64, bool) {
	if filepath.Ext(name) != walExt {
		return 0, false
	}
	t, err := time.ParseInLocation(bucketLayout, strings.TrimSuffix(name, walExt), time.UTC)
	if err != nil {
		return 0, false
	}
	return t.Unix() / bucketSeconds, true
}

func posTime(pos int64) time.Time { return time.Unix(pos*bucketSeconds, 0).UTC() }

func splitPos(pos int64) (int32, int32) {
	return int32(pos / bucketsPerDay), int32(pos % bucketsPerDay)
}
