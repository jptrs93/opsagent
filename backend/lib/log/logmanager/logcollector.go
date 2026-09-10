package logmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

var (
	reorderGraceWindow     = time.Minute
	commitSizeThresh       = int64(64_000_000)
	tailPollInterval       = time.Second
	commitTickInterval     = time.Minute
	fileListInterval       = 20 * time.Second
	collectorRetryInterval = 5 * time.Second
)

type LogStreamCollector struct {
	ctx               context.Context
	deploymentID      int32
	liveSpool         *LiveSegmentSpool
	db                *logdb.Queries
	onCommit          func(deploymentID, day int32)
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
	outs, err := writeArchiveFiles(context.Background(), dayDir, i.deploymentID, walSource{deploymentID: i.deploymentID, start: r.start, end: r.end}, 0)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return fmt.Errorf("no records found streaming spooled range %d/%d+%d..%d/%d+%d",
				r.start.day, r.start.bucket, r.start.byteOffset, r.end.day, r.end.bucket, r.end.byteOffset)
		}
		return err
	}
	out := &outs[0]
	final := archiveFileName(archiveLevelShredded, out.minTime, out.maxTime, out.node, out.seq)
	ctx := context.Background()
	err = i.db.Tx(ctx, func(q *logdb.Queries) error {
		if err := insertArchiveOutput(ctx, q, i.deploymentID, r.start.day, archiveLevelShredded, out); err != nil {
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
		_ = os.Remove(out.provisional)
		return err
	}
	if err := os.Rename(out.provisional, filepath.Join(dayDir, final)); err != nil {
		return err
	}
	if err := syncDir(dayDir); err != nil {
		return err
	}
	s.committed = r.end
	s.pruneAggregatesLocked(r.end)
	s.dropFirstLocked()
	i.deleteConsumedLogWALs(r.end)
	if i.onCommit != nil {
		i.onCommit(i.deploymentID, r.start.day)
	}
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
	if err := completePendingSwap(ctx, i.db, i.deploymentID, row); err != nil {
		return StreamMarker{}, err
	}
	return StreamMarker{day: int32(row.Day), bucket: int32(row.Bucket), byteOffset: row.ByteOffset, time: row.RecordTime}, nil
}

func completePendingSwap(ctx context.Context, db *logdb.Queries, deploymentID int32, row logdb.LogStreamCommitMarker) error {
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
		if !os.IsNotExist(err) {
			return err
		}
		_, lookupErr := db.GetLogFileBySeq(ctx, logdb.GetLogFileBySeqParams{DeploymentID: int64(deploymentID), Day: row.Day, Seq: seq})
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return nil
		}
		if lookupErr != nil {
			return lookupErr
		}
		return fmt.Errorf("committed archive file %q missing", row.File)
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
			if e.IsDir() || !strings.HasSuffix(e.Name(), archiveExt+tmpExt) {
				continue
			}
			if info, err := e.Info(); err == nil && clock().Sub(info.ModTime()) < rewriteGrace {
				continue
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (i *LogStreamCollector) hasUncollectedLogs() bool {
	return true
}

func newProducerCtx(parent context.Context, isProducing bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if !isProducing {
		cancel()
	}
	return ctx, cancel
}
