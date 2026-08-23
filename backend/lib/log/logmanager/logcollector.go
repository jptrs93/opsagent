package logmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

var (
	reorderGraceWindow     = time.Minute
	commitSizeThresh       = int64(400_000_000)
	tailPollInterval       = time.Second
	commitTickInterval     = time.Minute
	fileListInterval       = 20 * time.Second
	collectorRetryInterval = 5 * time.Second
)

// should be started for every existing log deployment directory manages the logs of a single deployment
type LogStreamCollector struct {
	// ctx is the component root logging context, tagged at construction and
	// carrying the deployment id.
	ctx               context.Context
	deploymentID      int32
	liveSpool         *LiveSegmentSpool
	db                *logdb.Queries
	collectorRunning  bool
	producerCount     int
	producerCtxCancel context.CancelFunc
	mu                sync.Mutex
}

func NewLogStreamCollector(deploymentID int32, db *logdb.Queries) *LogStreamCollector {
	return &LogStreamCollector{
		ctx:          logu.AddKV(logu.AddTag(context.Background(), "LogCollector"), "dep", deploymentID),
		deploymentID: deploymentID,
		liveSpool:    newLiveSegmentSpool(),
		db:           db,
	}
}

func (i *LogStreamCollector) AlignCollecting(runningCountChange int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.producerCount = max(0, i.producerCount+runningCountChange)
	if i.producerCount == 0 && i.producerCtxCancel != nil {
		i.producerCtxCancel()
	}
	if i.collectorRunning || (i.producerCount == 0 && !i.hasUncollectedLogs()) {
		return
	}
	i.collectorRunning = true
	var producerCtx context.Context
	producerCtx, i.producerCtxCancel = newProducerCtx(i.ctx, i.producerCount > 0)
	go i.runCollector(producerCtx)
}

func (i *LogStreamCollector) runCollector(producerCtx context.Context) {
	for {
		if err := i.RunCollectorOnce(producerCtx); err != nil {
			slog.WarnContext(producerCtx, "log stream collector failed", "err", err)
			time.Sleep(collectorRetryInterval)
			continue
		}
		i.mu.Lock()
		if i.producerCount == 0 {
			i.collectorRunning = false
			i.mu.Unlock()
			return
		}
		i.producerCtxCancel()
		producerCtx, i.producerCtxCancel = context.WithCancel(i.ctx)
		i.mu.Unlock()
	}
}

func (i *LogStreamCollector) RunCollectorOnce(producerCtx context.Context) error {
	m, err := i.loadCommittedMarker(context.Background())
	if err != nil {
		return err
	}
	i.removeOrphanTmpFiles()
	i.deleteConsumedLogWALs(m)
	i.liveSpool.Reset(m)
	ticker := time.NewTicker(commitTickInterval)
	defer ticker.Stop()
	stop := make(chan struct{})
	defer close(stop)
	tickErr := make(chan error, 1)
	go func() {
		// time based commit is to cover the case where there is no logs emitted by the application for a long period of time
		for {
			select {
			case <-stop:
				return
			case t := <-ticker.C:
				if err := i.commitOnTick(t); err != nil {
					select {
					case tickErr <- err:
					default:
					}
					return
				}
			}
		}
	}()
	var failed error
	for r, err := range StreamDeploymentLogRecords(producerCtx, i.deploymentID, m) {
		if err != nil {
			failed = fmt.Errorf("streaming log wals: %w", err)
			break
		}
		i.liveSpool.Add(r)
		if failed = i.CommitIfNeed(clock()); failed != nil {
			break
		}
		select {
		case failed = <-tickErr:
		default:
		}
		if failed != nil {
			break
		}
	}
	if failed != nil {
		return failed
	}
	select {
	case err := <-tickErr:
		return err
	default:
	}
	if err := i.CommitAll(); err != nil {
		return err
	}
	slog.InfoContext(producerCtx, "log stream ended gracefully")
	return nil
}

func (i *LogStreamCollector) CommitIfNeed(tNow time.Time) error {
	s := i.liveSpool
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ranges) == 0 {
		return nil
	}
	if len(s.ranges) > 1 && tNow.After(dayCommitDeadline(s.ranges[0].start.day)) {
		return i.commitSpooledChunk()
	}
	// todo: if we are close to the end of the day relative to range[0] then we should just wait till end to commit
	if s.ranges[0].size >= commitSizeThresh {
		return i.commitSpooledChunk()
	}
	return nil
}

func (i *LogStreamCollector) commitOnTick(tNow time.Time) error {
	s := i.liveSpool
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ranges) == 0 {
		return nil
	}
	if tNow.After(dayCommitDeadline(s.ranges[0].start.day)) {
		return i.commitSpooledChunk()
	}
	if s.ranges[0].size >= commitSizeThresh {
		return i.commitSpooledChunk()
	}
	return nil
}

func (i *LogStreamCollector) CommitAll() error {
	s := i.liveSpool
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.ranges) > 0 {
		n := len(s.ranges)
		if err := i.commitSpooledChunk(); err != nil {
			return err
		}
		if len(s.ranges) == n {
			return nil
		}
	}
	return nil
}

func (i *LogStreamCollector) commitSpooledChunk() error {
	s := i.liveSpool
	r := s.ranges[0]
	dayDir := archiveDayDir(i.deploymentID, r.start.day)
	if err := os.MkdirAll(dayDir, 0o750); err != nil {
		return err
	}
	seq := newArchiveSeq()
	provisional := filepath.Join(dayDir, provisionalFileName(seq))
	w, err := newArchiveWriter(provisional)
	if err != nil {
		return err
	}
	var node int32
	for rec, err := range sortedByTime(StreamDeploymentLogRecordsRange(i.deploymentID, r.start, r.end)) {
		if err == nil {
			if w.count == 0 {
				node = rec.record.Node
			}
			level, msg := shredFields(rec.record)
			err = w.append(logRow{
				Time:            rec.record.Time,
				Version:         rec.record.Version,
				Run:             rec.record.Run,
				Node:            rec.record.Node,
				InstanceOrdinal: rec.record.InstanceOrdinal,
				Stream:          rec.record.Stream,
				Level:           level,
				Msg:             msg,
				RawMessage:      rec.record.Line,
			})
		}
		if err != nil {
			w.abort()
			_ = os.Remove(provisional)
			return err
		}
	}
	if w.count == 0 {
		w.abort()
		_ = os.Remove(provisional)
		return fmt.Errorf("no records found streaming spooled range %d/%d+%d..%d/%d+%d",
			r.start.day, r.start.bucket, r.start.byteOffset, r.end.day, r.end.bucket, r.end.byteOffset)
	}
	if err := w.finish(map[string]string{"deployment": strconv.Itoa(int(i.deploymentID))}); err != nil {
		_ = os.Remove(provisional)
		return err
	}
	info, err := os.Stat(provisional)
	if err != nil {
		return err
	}
	final := archiveFileName(archiveLevelBatch, w.minTime, w.maxTime, node, seq)
	ctx := context.Background()
	err = i.db.Tx(ctx, func(q *logdb.Queries) error {
		if _, err := q.InsertLogFile(ctx, logdb.InsertLogFileParams{
			DeploymentID: int64(i.deploymentID),
			Day:          int64(r.start.day),
			Level:        archiveLevelBatch,
			Node:         int64(node),
			Seq:          seq,
			MinTime:      w.minTime,
			MaxTime:      w.maxTime,
			RowCount:     w.count,
			ByteSize:     info.Size(),
			CreatedAt:    clock().UnixMilli(),
		}); err != nil {
			return err
		}
		return q.UpsertLogStreamCommitMarker(ctx, logdb.UpsertLogStreamCommitMarkerParams{
			DeploymentID: int64(i.deploymentID),
			Day:          int64(r.end.day),
			Bucket:       int64(r.end.bucket),
			RecordTime:   r.end.time,
			ByteOffset:   r.end.byteOffset,
			UpdatedAt:    clock().UnixMilli(),
			File:         final,
		})
	})
	if err != nil {
		_ = os.Remove(provisional)
		return err
	}
	if err := os.Rename(provisional, filepath.Join(dayDir, final)); err != nil {
		return err
	}
	if err := syncDir(dayDir); err != nil {
		return err
	}
	s.committed = r.end
	s.dropFirstLocked()
	i.deleteConsumedLogWALs(r.end)
	return nil
}

func (i *LogStreamCollector) deleteConsumedLogWALs(m StreamMarker) {
	if m.isZero() {
		return
	}
	q, err := listExistingFrom(i.deploymentID, 0, 0)
	if err != nil {
		return
	}
	for _, ls := range q {
		if ls.day < m.day || (ls.day == m.day && ls.bucket < m.bucket) {
			_ = os.Remove(ls.filePath)
			continue
		}
		if ls.day == m.day && ls.bucket == m.bucket && walBucketConsumed(ls, m) {
			_ = os.Remove(ls.filePath)
		}
		return
	}
}

func walBucketConsumed(ls LogSourceRef, m StreamMarker) bool {
	if !clock().After(ls.bucketEnd.Add(reorderGraceWindow)) {
		return false
	}
	f, err := os.Open(ls.filePath)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	rest := st.Size() - m.byteOffset
	if rest <= 0 || rest > int64(logv2.RecordMaxLen) {
		return false
	}
	buf := make([]byte, rest)
	if _, err := f.ReadAt(buf, m.byteOffset); err != nil {
		return false
	}
	rec, size, status := parseWalRecord(buf)
	if status != parseOK || rec.Time != m.time {
		return false
	}
	return int64(size) == rest
}

func (i *LogStreamCollector) loadCommittedMarker(ctx context.Context) (StreamMarker, error) {
	row, err := i.db.GetLogStreamCommitMarker(ctx, int64(i.deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return StreamMarker{}, nil
	}
	if err != nil {
		return StreamMarker{}, err
	}
	if err := completePendingSwap(i.deploymentID, row); err != nil {
		return StreamMarker{}, err
	}
	return StreamMarker{day: int32(row.Day), bucket: int32(row.Bucket), byteOffset: row.ByteOffset, time: row.RecordTime}, nil
}

func completePendingSwap(deploymentID int32, row logdb.LogStreamCommitMarker) error {
	if row.File == "" {
		return nil
	}
	dayDir := archiveDayDir(deploymentID, int32(row.Day))
	final := filepath.Join(dayDir, row.File)
	if _, err := os.Stat(final); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	seq, ok := parseArchiveSeq(row.File)
	if !ok {
		return fmt.Errorf("unparseable committed archive file name %q", row.File)
	}
	provisional := filepath.Join(dayDir, provisionalFileName(seq))
	if _, err := os.Stat(provisional); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("committed archive file %q missing", row.File)
		}
		return err
	}
	if err := os.Rename(provisional, final); err != nil {
		return err
	}
	return syncDir(dayDir)
}

func (i *LogStreamCollector) removeOrphanTmpFiles() {
	dayDirs, err := os.ReadDir(archiveDeploymentDir(i.deploymentID))
	if err != nil {
		return
	}
	for _, d := range dayDirs {
		if !d.IsDir() {
			continue
		}
		day, ok := parseDayDirName(d.Name())
		if !ok {
			continue
		}
		dir := archiveDayDir(i.deploymentID, day)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), archiveExt+tmpExt) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

func (i *LogStreamCollector) hasUncollectedLogs() bool {
	// for now it harmless to always attempt to collect
	return true
}

func newProducerCtx(parent context.Context, isProducing bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if !isProducing {
		cancel()
	}
	return ctx, cancel
}
