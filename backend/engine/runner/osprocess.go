package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

type osProcessRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store storage.OperatorStore
	id    *apigen.DeploymentIdentifier
	seqNo int32

	workDir    string
	runAs      string
	outputPath string
	binPath    string
	adoptPid   int // 0 unless this runner was created to adopt an existing PID

	stopping atomic.Bool

	pidMu sync.Mutex
	pid   int

	restartsMu  sync.Mutex
	restarts    int32
	lastRestart time.Time
}

const (
	osProcessMinBackoff      = 1 * time.Second
	osProcessMaxBackoff      = 30 * time.Second
	osProcessStableRunWindow = 15 * time.Second
)

// newOSProcessRunner constructs and starts an OS-process runner for dep.
// When prev is non-nil the runner adopts the previously-running PID; the
// restart counters are carried over untouched and the initial STARTING
// write is skipped because the process has not actually restarted.
func newOSProcessRunner(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, prev *apigen.RunnerStatus) *osProcessRunner {
	ctx, cancel := context.WithCancel(parentCtx)

	runAs := resolveRunAs(osProcessRunAs(dep))
	workDir, err := resolveWorkingDir(osProcessWorkingDir(dep), runAs)
	if err != nil {
		slog.ErrorContext(ctx, "resolving working dir", "err", err)
		workDir = ""
	}

	seqNo := dep.SeqNo
	if prev != nil {
		seqNo = prev.DeploymentSeqNo
	}

	r := &osProcessRunner{
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		store:      store,
		id:         dep.ID,
		seqNo:      seqNo,
		workDir:    workDir,
		runAs:      runAs,
		outputPath: dep.RunOutputPath(),
		binPath:    artifact,
	}

	if prev != nil {
		r.adoptPid = int(prev.RunningPid)
		r.pid = r.adoptPid
		r.restarts = prev.NumberOfRestarts
		r.lastRestart = prev.LastRestartAt
		// Don't rewrite RunnerStatus here; the monitor loop will pick up
		// the current state and write transitions as they happen.
	} else {
		r.writeInitialStarting()
	}

	go r.run()
	return r
}

func (r *osProcessRunner) SeqNo() int32 { return r.seqNo }

func (r *osProcessRunner) Stop() {
	if !r.stopping.CompareAndSwap(false, true) {
		<-r.done
		return
	}
	// Wake the backoff sleep.
	r.cancel()

	pid := r.currentPID()
	if pid > 0 {
		if err := signalDaemonTerminate(pid); err != nil && !isProcessGone(err) {
			slog.WarnContext(r.ctx, "sending terminate signal failed", "pid", pid, "err", err)
		}
		// Grace period: 3s for SIGTERM, then SIGKILL.
		select {
		case <-r.done:
			return
		case <-time.After(3 * time.Second):
		}
		if err := signalDaemonKill(pid); err != nil && !isProcessGone(err) {
			slog.WarnContext(r.ctx, "force killing process failed", "pid", pid, "err", err)
		}
	}
	<-r.done
}

func (r *osProcessRunner) run() {
	defer close(r.done)

	crashCount := 0

	// If we were constructed to adopt an existing PID, the first iteration
	// polls that process rather than spawning a new one. On exit we drop
	// through to the normal spawn loop.
	if r.adoptPid > 0 {
		slog.InfoContext(r.ctx, "adopting existing process", "pid", r.adoptPid, "log", r.outputPath)
		r.monitorAdoptedProcess(r.adoptPid)
		if r.stopping.Load() {
			r.writeStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}
		r.bumpRestarts()
		r.writeStatus(apigen.RunningStatus_CRASHED, r.adoptPid)
		crashCount = 1
		if !r.sleepBackoff(crashCount) {
			r.writeStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}
	}

	for {
		if r.stopping.Load() {
			r.writeStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		pid, err := spawnDaemon(r.binPath, r.workDir, r.outputPath, r.runAs)
		if err != nil {
			slog.ErrorContext(r.ctx, "spawning daemon failed", "err", err, "bin", r.binPath)
			r.setCurrentPID(0)
			r.writeStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.writeStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		r.setCurrentPID(pid)
		slog.InfoContext(r.ctx, "daemon started", "pid", pid, "log", r.outputPath, "bin", r.binPath, "workDir", r.workDir)
		r.writeStatus(apigen.RunningStatus_RUNNING, pid)
		startedAt := time.Now()

		awaitProcessExit(pid)

		if r.stopping.Load() {
			r.writeStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		// Stability reset: if the process ran long enough before crashing,
		// reset the crash counter so a one-off crash doesn't escalate into
		// a 30s backoff forever.
		if time.Since(startedAt) >= osProcessStableRunWindow {
			crashCount = 0
		}
		crashCount++
		r.bumpRestarts()
		r.writeStatus(apigen.RunningStatus_CRASHED, pid)

		if !r.sleepBackoff(crashCount) {
			r.writeStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}
	}
}

// monitorAdoptedProcess polls an adopted PID until it exits or the runner is
// stopped. Adopted processes were not forked by us, so Wait4 is not usable.
func (r *osProcessRunner) monitorAdoptedProcess(pid int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if r.stopping.Load() {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			exists, err := processExists(pid)
			if err != nil {
				slog.WarnContext(r.ctx, "failed checking adopted process liveness", "pid", pid, "err", err)
				continue
			}
			if !exists {
				return
			}
		}
	}
}

func (r *osProcessRunner) sleepBackoff(crashCount int) bool {
	delay := computeOSProcessBackoff(crashCount)
	slog.InfoContext(r.ctx, "backoff sleep before respawn", "delay", delay, "crashes", crashCount)
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func computeOSProcessBackoff(crashCount int) time.Duration {
	if crashCount <= 1 {
		return osProcessMinBackoff
	}
	delay := osProcessMinBackoff
	for i := 1; i < crashCount; i++ {
		delay *= 2
		if delay >= osProcessMaxBackoff {
			return osProcessMaxBackoff
		}
	}
	return delay
}

// --- state writes ---

// writeInitialStarting seeds the first RunnerStatus for a fresh run. If the
// existing status is already on this dep.SeqNo (stop+start of the same
// version), NumberOfRestarts is bumped; otherwise counters start at zero.
func (r *osProcessRunner) writeInitialStarting() {
	r.store.MustWriteDeploymentStatus(context.Background(), *r.id, func(s *apigen.DeploymentStatus) {
		if s.Runner != nil && s.Runner.DeploymentSeqNo > r.seqNo {
			return
		}
		var restarts int32
		var lastRestart time.Time
		if s.Runner != nil && s.Runner.DeploymentSeqNo == r.seqNo {
			restarts = s.Runner.NumberOfRestarts + 1
			lastRestart = time.Now()
		}
		r.restartsMu.Lock()
		r.restarts = restarts
		r.lastRestart = lastRestart
		r.restartsMu.Unlock()

		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = r.id
		s.Runner = &apigen.RunnerStatus{
			DeploymentSeqNo:  r.seqNo,
			Status:           apigen.RunningStatus_STARTING,
			RunningArtifact:  r.binPath,
			NumberOfRestarts: restarts,
			LastRestartAt:    lastRestart,
		}
	})
}

func (r *osProcessRunner) writeStatus(status apigen.RunningStatus, pid int) {
	restarts, lastRestart := r.loadRestartState()
	r.store.MustWriteDeploymentStatus(context.Background(), *r.id, func(s *apigen.DeploymentStatus) {
		if s.Runner != nil && s.Runner.DeploymentSeqNo > r.seqNo {
			return
		}
		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = r.id
		s.Runner = &apigen.RunnerStatus{
			DeploymentSeqNo:  r.seqNo,
			Status:           status,
			RunningPid:       int32(pid),
			RunningArtifact:  r.binPath,
			NumberOfRestarts: restarts,
			LastRestartAt:    lastRestart,
		}
	})
}

func (r *osProcessRunner) bumpRestarts() {
	r.restartsMu.Lock()
	r.restarts++
	r.lastRestart = time.Now()
	r.restartsMu.Unlock()
}

func (r *osProcessRunner) loadRestartState() (int32, time.Time) {
	r.restartsMu.Lock()
	defer r.restartsMu.Unlock()
	return r.restarts, r.lastRestart
}

func (r *osProcessRunner) setCurrentPID(pid int) {
	r.pidMu.Lock()
	r.pid = pid
	r.pidMu.Unlock()
}

func (r *osProcessRunner) currentPID() int {
	r.pidMu.Lock()
	defer r.pidMu.Unlock()
	return r.pid
}

// --- helpers ---

func resolveWorkingDir(dir, runAs string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	if runAs != "" {
		u, err := user.Lookup(runAs)
		if err != nil {
			return "", fmt.Errorf("looking up user %q: %v", runAs, err)
		}
		return u.HomeDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir failed: %v", err)
	}
	return home, nil
}

// resolveRunAs returns the OS user to run the deployment process as. In
// local dev mode, returns "" (inherit opsagent's user). Otherwise, defaults
// to "ubuntu".
func resolveRunAs(configRunAs string) string {
	if ainit.Config.IsLocalDev == "true" {
		return ""
	}
	if configRunAs != "" {
		return configRunAs
	}
	return "ubuntu"
}

func osProcessWorkingDir(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec == nil || dep.Spec.Runner == nil || dep.Spec.Runner.OsProcess == nil {
		return ""
	}
	return dep.Spec.Runner.OsProcess.WorkingDir
}

func osProcessRunAs(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec == nil || dep.Spec.Runner == nil || dep.Spec.Runner.OsProcess == nil {
		return ""
	}
	return dep.Spec.Runner.OsProcess.RunAs
}
