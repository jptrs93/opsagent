package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
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

	store        storage.OperatorStore
	deploymentID int32

	// these fields are not part of the runnerStatus they are derived from the version of the deploymentConfig
	workDir       string // working directory where binary is executed from
	runAs         string // the unix user the process is run as
	outputPath    string // where stdout/stderr of process is streamed to
	leavePrevious bool   // skip terminating old process on upgrade (app handles its own rollover)

	status apigen.RunnerStatus

	stopping atomic.Bool
}

const (
	osProcessMinBackoff      = 1 * time.Second
	osProcessMaxBackoff      = 60 * time.Second
	osProcessStableRunWindow = 15 * time.Second
)

// reAttachOSProcessRunner attaches to existing process runner
func reAttachOSProcessRunner(parentCtx context.Context, store storage.OperatorStore, deploymentConfig apigen.DeploymentConfig, runnerStatus apigen.RunnerStatus) *osProcessRunner {
	ctx, cancel := context.WithCancel(parentCtx)
	// todo: potential gap if the deploymentConfig has changed then these resolutions could be different from the version actually still running
	runAs := resolveRunAs(osProcessRunAs(&deploymentConfig))
	workDir := resolveWorkingDir(ctx, osProcessWorkingDir(&deploymentConfig), runAs)
	r := &osProcessRunner{
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
		store:         store,
		deploymentID:  deploymentConfig.ID,
		workDir:       workDir,
		runAs:         runAs,
		outputPath:    apigen.RunOutputFile(deploymentConfig.ID, runnerStatus.DeploymentConfigVersion),
		leavePrevious: osProcessStrategy(&deploymentConfig) == "leavePrevious",
		status:        runnerStatus,
	}
	go r.run()
	return r
}

// newOSProcessRunner upgrades the runner to the prepared config version and starts process runner
func newOSProcessRunner(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *osProcessRunner {
	ctx, cancel := context.WithCancel(parentCtx)

	runAs := resolveRunAs(osProcessRunAs(dep))
	workDir := resolveWorkingDir(ctx, osProcessWorkingDir(dep), runAs)

	configVersion := dep.Version

	r := &osProcessRunner{
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
		store:         store,
		deploymentID:  dep.ID,
		workDir:       workDir,
		runAs:         runAs,
		outputPath:    apigen.RunOutputFile(dep.ID, configVersion),
		leavePrevious: osProcessStrategy(dep) == "leavePrevious",
		status: apigen.RunnerStatus{
			DeploymentConfigVersion: configVersion,
			RunningPid:              0,
			RunningArtifact:         preparerStatus.Artifact,
			Status:                  apigen.RunningStatus_STARTING,
			NumberOfRestarts:        0,
			LastRestartAt:           time.Now(),
		},
	}
	r.writeStatus()
	go r.run()
	return r
}

func (r *osProcessRunner) Version() int32 { return r.status.DeploymentConfigVersion }

func (r *osProcessRunner) Stop() {
	if !r.stopping.CompareAndSwap(false, true) {
		<-r.done
		return
	}
	r.cancel()

	pid := int(atomic.LoadInt32(&r.status.RunningPid))
	if pid > 0 && !r.leavePrevious {
		if err := signalDaemonTerminate(pid); err != nil && !isProcessGone(err) {
			slog.Warn("sending terminate signal failed", "pid", pid, "err", err)
		}
		select {
		case <-r.done:
			return
		case <-time.After(3 * time.Second):
		}
		if err := signalDaemonKill(pid); err != nil && !isProcessGone(err) {
			slog.Warn("force killing process failed", "pid", pid, "err", err)
		}
		<-r.done
	}
}

func (r *osProcessRunner) run() {
	defer close(r.done)

	crashCount := 0

	// If we were constructed to adopt an existing PID (via reAttachOSProcessRunner),
	// the first iteration polls that process rather than spawning a new one.
	// On exit we drop through to the normal spawn loop.
	adoptPid := int(r.status.RunningPid)
	if adoptPid > 0 {
		slog.InfoContext(r.ctx, "adopting existing process", "pid", adoptPid, "log", r.outputPath)
		r.monitorAdoptedProcess(adoptPid)
	}

	for {
		if r.stopping.Load() {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		pid, err := spawnDaemon(r.status.RunningArtifact, r.workDir, r.outputPath, r.runAs)
		if err != nil {
			slog.ErrorContext(r.ctx, "spawning daemon failed", "err", err, "bin", r.status.RunningArtifact, "workDir", r.workDir, "runAs", r.runAs)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		atomic.StoreInt32(&r.status.RunningPid, int32(pid))
		slog.InfoContext(r.ctx, "daemon started", "pid", pid, "log", r.outputPath, "bin", r.status.RunningArtifact, "workDir", r.workDir)
		r.updateStatus(apigen.RunningStatus_RUNNING, int32(pid))
		startedAt := time.Now()

		r.awaitProcessOrCancel(pid)

		if r.stopping.Load() {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		// Stability reset: if the process ran long enough before crashing,
		// reset the crash counter so a one-off crash doesn't escalate into
		// a 30s backoff forever.
		if time.Since(startedAt) >= osProcessStableRunWindow {
			crashCount = 0
		}
		crashCount++
		r.updateStatus(apigen.RunningStatus_CRASHED, int32(pid))

		if !r.sleepBackoff(crashCount) {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		r.status.NumberOfRestarts++
		r.status.LastRestartAt = time.Now()
	}
}

// monitorAdoptedProcess polls an adopted PID until it exits or the runner is
// stopped. Adopted processes were not forked by us, so Wait4 is not usable.
func (r *osProcessRunner) monitorAdoptedProcess(pid int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	errCount := 0
	const maxErrors = 15 // give up after ~30s of persistent errors
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
				errCount++
				slog.WarnContext(r.ctx, "failed checking adopted process liveness", "pid", pid, "err", err, "errCount", errCount)
				if errCount >= maxErrors {
					slog.ErrorContext(r.ctx, "giving up on adopted process after persistent errors", "pid", pid)
					return
				}
				continue
			}
			errCount = 0
			if !exists {
				return
			}
		}
	}
}

// awaitProcessOrCancel waits for the process to exit. If the context is
// cancelled (Stop was called), it returns immediately — Stop handles
// signalling the process when needed.
func (r *osProcessRunner) awaitProcessOrCancel(pid int) {
	exited := make(chan struct{})
	go func() {
		awaitProcessExit(pid)
		close(exited)
	}()
	select {
	case <-exited:
	case <-r.ctx.Done():
	}
}

func (r *osProcessRunner) sleepBackoff(crashCount int) bool {
	delay := computeOSProcessBackoff(int(crashCount))
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

func (r *osProcessRunner) updateStatus(status apigen.RunningStatus, pid int32) {
	r.status.Status = status
	r.status.RunningPid = pid
	r.writeStatus()
}

func (r *osProcessRunner) writeStatus() {
	r.store.MustWriteDeploymentStatus(context.Background(), r.deploymentID, func(s *apigen.DeploymentStatus) {
		if s.Runner != nil && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded runner")
			return
		}
		s.StatusSeqNo++
		s.Timestamp = time.Now()
		s.DeploymentID = r.deploymentID
		s.Runner = &r.status
	})
}

// --- helpers ---

func resolveWorkingDir(ctx context.Context, dir, runAs string) string {
	if dir != "" {
		return dir
	}
	if runAs != "" {
		u, err := user.Lookup(runAs)
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("resolving working dir: looking up user %v: %v", u, err))
			return ""
		}
		return u.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.ErrorContext(ctx, "resolving home dir:", "err", err)
		return ""
	}
	return home
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

func osProcessStrategy(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec == nil || dep.Spec.Runner == nil || dep.Spec.Runner.OsProcess == nil {
		return ""
	}
	return dep.Spec.Runner.OsProcess.Strategy
}
