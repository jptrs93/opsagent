package logmanager

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/goutil/contextu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

var (
	rewriteGrace      = time.Minute
	reconcileInterval = time.Hour
	maintenanceTick   = time.Minute
	backfillPause     = 2 * time.Second
	backfillBackoff   = time.Hour
	retentionDays     = int32(30)
	rollupTargetBytes = int64(256 << 20)
	rollupDayEndDelay = 15 * time.Minute
	backfillEnabled   = true
)

type rollupJob struct {
	deploymentID int32
	day          int32
}

func (m *Manager) nudgeMaintenance(int32, int32) {
	select {
	case m.nudge <- struct{}{}:
	default:
	}
}

func (m *Manager) runMaintenance(ctx context.Context) {
	defer close(m.maintStopped)
	var lastReconcile time.Time
	for ctx.Err() == nil {
		if lastReconcile.IsZero() || clock().Sub(lastReconcile) >= reconcileInterval {
			m.reconcile(ctx)
			lastReconcile = clock()
		}
		did, err := m.maintenanceStep(ctx)
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(m.ctx, "log maintenance step failed", "err", err)
			contextu.Sleep(ctx, collectorRetryInterval)
			continue
		}
		if did {
			continue
		}
		select {
		case <-ctx.Done():
		case <-m.nudge:
		case <-time.After(maintenanceTick):
		}
	}
}

func (m *Manager) maintenanceStep(ctx context.Context) (bool, error) {
	if job, ok, err := m.nextRollup(ctx); err != nil {
		return false, err
	} else if ok {
		return true, m.runRollup(ctx, job)
	}
	if !backfillEnabled {
		return false, nil
	}
	if err := m.announceBackfill(ctx); err != nil {
		return false, err
	}
	f, ok, err := m.nextBackfill(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, m.reportBackfillIdle(ctx)
	}
	err = m.backfill(ctx, f)
	contextu.Sleep(ctx, backfillPause)
	return true, err
}

type backfillProgress struct {
	announced bool
	done      int
	finished  bool
	blocked   bool
}

func (m *Manager) announceBackfill(ctx context.Context) error {
	if m.backfillProg.announced {
		return nil
	}
	c, err := m.db.CountLevelZeroFiles(ctx)
	if err != nil {
		return err
	}
	m.backfillProg.announced = true
	if c.FileCount == 0 {
		m.backfillProg.finished = true
		return nil
	}
	slog.InfoContext(m.ctx, "log backfill pending", "files", c.FileCount, "bytes", c.ByteTotal)
	return nil
}

func (m *Manager) reportBackfillIdle(ctx context.Context) error {
	if m.backfillProg.finished {
		return nil
	}
	c, err := m.db.CountLevelZeroFiles(ctx)
	if err != nil {
		return err
	}
	if c.FileCount == 0 {
		m.backfillProg.finished = true
		slog.InfoContext(m.ctx, "log backfill complete", "files", m.backfillProg.done)
		return nil
	}
	if !m.backfillProg.blocked {
		m.backfillProg.blocked = true
		slog.WarnContext(m.ctx, "log backfill blocked on files that failed to rewrite", "files", c.FileCount, "bytes", c.ByteTotal, "done", m.backfillProg.done)
	}
	return nil
}

func dayEnd(day int32) time.Time {
	return time.Unix(int64(day+1)*daySeconds, 0).UTC()
}

func (m *Manager) nextRollup(ctx context.Context) (rollupJob, bool, error) {
	days, err := m.db.ListLevelOneDays(ctx)
	if err != nil {
		return rollupJob{}, false, err
	}
	now := clock()
	for _, d := range days {
		if d.FileCount < 2 {
			continue
		}
		if int64(d.ByteTotal.Float64) >= rollupTargetBytes || now.After(dayEnd(int32(d.Day)).Add(rollupDayEndDelay)) {
			return rollupJob{deploymentID: int32(d.DeploymentID), day: int32(d.Day)}, true, nil
		}
	}
	return rollupJob{}, false, nil
}

func (m *Manager) runRollup(ctx context.Context, job rollupJob) error {
	inputs, err := m.db.ListLogFilesForDayLevel(ctx, logdb.ListLogFilesForDayLevelParams{
		DeploymentID: int64(job.deploymentID), Day: int64(job.day), Level: archiveLevelShredded,
	})
	if err != nil {
		return err
	}
	if len(inputs) < 2 {
		return nil
	}
	var totalBytes, totalRows int64
	paths := make([]string, 0, len(inputs))
	for _, f := range inputs {
		totalBytes += f.ByteSize
		totalRows += f.RowCount
		paths = append(paths, archiveFilePath(job.deploymentID, f))
	}
	dayDir := archiveDayDir(job.deploymentID, job.day)
	if err := ensureFreeSpace(dayDir, 2*totalBytes); err != nil {
		return err
	}
	var maxRows int64
	if totalBytes > rollupTargetBytes {
		maxRows = (totalRows*rollupTargetBytes + totalBytes - 1) / totalBytes
	}
	outs, err := writeArchiveFiles(ctx, dayDir, job.deploymentID, mergeSource{paths: paths}, maxRows)
	if err != nil {
		return err
	}
	if err := m.commitRewrite(ctx, job.deploymentID, job.day, archiveLevelRollup, inputs, outs); err != nil {
		return err
	}
	slog.InfoContext(m.ctx, "rolled up log files", "dep", job.deploymentID, "day", dayDirName(job.day), "inputs", len(inputs), "outputs", len(outs), "bytes", totalBytes)
	return nil
}

func (m *Manager) nextBackfill(ctx context.Context) (logdb.LogFile, bool, error) {
	files, err := m.db.ListLevelZeroNewestFirst(ctx)
	if err != nil {
		return logdb.LogFile{}, false, err
	}
	now := clock()
	for _, f := range files {
		if until, skipped := m.skipUntil[f.ID]; skipped && now.Before(until) {
			continue
		}
		return f, true, nil
	}
	return logdb.LogFile{}, false, nil
}

func (m *Manager) backfill(ctx context.Context, f logdb.LogFile) error {
	dep, day := int32(f.DeploymentID), int32(f.Day)
	dayDir := archiveDayDir(dep, day)
	err := func() error {
		if err := ensureFreeSpace(dayDir, 2*f.ByteSize); err != nil {
			return err
		}
		outs, err := writeArchiveFiles(ctx, dayDir, dep, fileSource{path: archiveFilePath(dep, f)}, 0)
		if err != nil {
			return err
		}
		return m.commitRewrite(ctx, dep, day, archiveLevelShredded, []logdb.LogFile{f}, outs)
	}()
	if err != nil {
		m.skipUntil[f.ID] = clock().Add(backfillBackoff)
		return err
	}
	delete(m.skipUntil, f.ID)
	m.backfillProg.done++
	m.backfillProg.blocked = false
	slog.InfoContext(m.ctx, "backfilled log file", "dep", dep, "file", logFileName(f))
	return nil
}

func (m *Manager) commitRewrite(ctx context.Context, deploymentID, day int32, level int, inputs []logdb.LogFile, outs []archiveOutput) error {
	dayDir := archiveDayDir(deploymentID, day)
	finals := make([]string, 0, len(outs))
	removeFinals := func() {
		for _, p := range finals {
			_ = os.Remove(p)
		}
	}
	for i := range outs {
		final, err := finalizeOutput(dayDir, level, &outs[i])
		if err != nil {
			removeFinals()
			for j := i; j < len(outs); j++ {
				_ = os.Remove(outs[j].provisional)
			}
			return err
		}
		finals = append(finals, final)
	}
	old := make([]string, 0, len(inputs))
	for _, in := range inputs {
		old = append(old, archiveFilePath(deploymentID, in))
	}
	m.setPendingUnlink(old, true)
	if err := syncDir(dayDir); err != nil {
		removeFinals()
		m.setPendingUnlink(old, false)
		return err
	}
	if err := verifyRewrite(inputs, outs); err != nil {
		removeFinals()
		m.setPendingUnlink(old, false)
		return err
	}
	err := m.db.Tx(ctx, func(q *logdb.Queries) error {
		for i := range outs {
			if err := insertArchiveOutput(ctx, q, deploymentID, day, level, &outs[i]); err != nil {
				return err
			}
		}
		for _, in := range inputs {
			if err := q.DeleteLogFileKeys(ctx, in.ID); err != nil {
				return err
			}
			if err := q.DeleteLogFile(ctx, in.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		removeFinals()
		m.setPendingUnlink(old, false)
		return err
	}
	m.unlinkAfterGrace(old)
	return nil
}

func (m *Manager) setPendingUnlink(paths []string, pending bool) {
	m.unlinkMu.Lock()
	defer m.unlinkMu.Unlock()
	for _, p := range paths {
		if pending {
			m.pendingUnlink[p] = struct{}{}
		} else {
			delete(m.pendingUnlink, p)
		}
	}
}

func (m *Manager) isPendingUnlink(path string) bool {
	m.unlinkMu.Lock()
	defer m.unlinkMu.Unlock()
	_, ok := m.pendingUnlink[path]
	return ok
}

func (m *Manager) unlinkAfterGrace(paths []string) {
	m.unlinkWG.Add(1)
	time.AfterFunc(rewriteGrace, func() {
		defer m.unlinkWG.Done()
		for _, p := range paths {
			_ = os.Remove(p)
		}
		m.setPendingUnlink(paths, false)
	})
}

func (m *Manager) reconcile(ctx context.Context) {
	if err := m.sweepRowlessFiles(ctx); err != nil {
		slog.WarnContext(m.ctx, "log archive sweep failed", "err", err)
	}
	if err := m.sweepFilelessRows(ctx); err != nil {
		slog.WarnContext(m.ctx, "log catalog sweep failed", "err", err)
	}
	if err := m.applyRetention(ctx); err != nil {
		slog.WarnContext(m.ctx, "log retention failed", "err", err)
	}
}

func listDeploymentDirs(root string) []int32 {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []int32
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		out = append(out, int32(id))
	}
	return out
}

func (m *Manager) sweepRowlessFiles(ctx context.Context) error {
	now := clock()
	for _, dep := range listDeploymentDirs(ainit.StaticConfig.LogArchiveDir) {
		dayDirs, err := os.ReadDir(archiveDeploymentDir(dep))
		if err != nil {
			continue
		}
		for _, d := range dayDirs {
			if !d.IsDir() {
				continue
			}
			day, ok := parseDayDirName(d.Name())
			if !ok {
				continue
			}
			rows, err := m.db.ListLogFilesForDay(ctx, logdb.ListLogFilesForDayParams{DeploymentID: int64(dep), Day: int64(day)})
			if err != nil {
				return err
			}
			known := make(map[int64]bool, len(rows))
			for _, r := range rows {
				known[r.Seq] = true
			}
			dir := archiveDayDir(dep, day)
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil || now.Sub(info.ModTime()) < rewriteGrace {
					continue
				}
				name := e.Name()
				if strings.HasSuffix(name, tmpExt) {
					_ = os.Remove(filepath.Join(dir, name))
					continue
				}
				seq, ok := parseArchiveSeq(name)
				if !ok || known[seq] || m.isPendingUnlink(filepath.Join(dir, name)) {
					continue
				}
				slog.InfoContext(m.ctx, "removing archive file without catalog row", "dep", dep, "file", name)
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
	return nil
}

func (m *Manager) sweepFilelessRows(ctx context.Context) error {
	deps, err := m.db.ListLogFileDeployments(ctx)
	if err != nil {
		return err
	}
	now := clock()
	for _, dep := range deps {
		rows, err := m.db.ListLogFilesNewestFirst(ctx, dep)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if now.Sub(time.UnixMilli(r.CreatedAt)) < rewriteGrace {
				continue
			}
			if _, err := os.Stat(archiveFilePath(int32(dep), r)); err == nil || !os.IsNotExist(err) {
				continue
			}
			slog.WarnContext(m.ctx, "removing catalog row without archive file", "dep", dep, "file", logFileName(r))
			err := m.db.Tx(ctx, func(q *logdb.Queries) error {
				if err := q.DeleteLogFileKeys(ctx, r.ID); err != nil {
					return err
				}
				return q.DeleteLogFile(ctx, r.ID)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) applyRetention(ctx context.Context) error {
	cutoff := int32(clock().Unix()/daySeconds) - retentionDays
	days, err := m.db.ListLogFileDaysBefore(ctx, int64(cutoff))
	if err != nil {
		return err
	}
	for _, d := range days {
		err := m.db.Tx(ctx, func(q *logdb.Queries) error {
			if err := q.DeleteLogFileKeysForDay(ctx, logdb.DeleteLogFileKeysForDayParams{DeploymentID: d.DeploymentID, Day: d.Day}); err != nil {
				return err
			}
			return q.DeleteLogFilesForDay(ctx, logdb.DeleteLogFilesForDayParams{DeploymentID: d.DeploymentID, Day: d.Day})
		})
		if err != nil {
			return err
		}
	}
	for _, dep := range listDeploymentDirs(ainit.StaticConfig.LogArchiveDir) {
		dayDirs, err := os.ReadDir(archiveDeploymentDir(dep))
		if err != nil {
			continue
		}
		for _, d := range dayDirs {
			day, ok := parseDayDirName(d.Name())
			if !d.IsDir() || !ok || day >= cutoff {
				continue
			}
			_ = os.RemoveAll(archiveDayDir(dep, day))
		}
	}
	cutoffTime := time.Unix(int64(cutoff)*daySeconds, 0).UTC()
	for _, dep := range listDeploymentDirs(ainit.StaticConfig.LogWALDir) {
		dir := walDeploymentDir(dep)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if pos, ok := parseBucketPos(name); ok {
				if posTime(pos + 1).Before(cutoffTime) {
					_ = os.Remove(filepath.Join(dir, name))
				}
				continue
			}
			if strings.HasSuffix(name, ".logbin") {
				if info, err := e.Info(); err == nil && info.ModTime().Before(cutoffTime) {
					_ = os.Remove(filepath.Join(dir, name))
				}
			}
		}
	}
	return nil
}
