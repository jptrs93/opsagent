package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"sync/atomic"
	"time"

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
	workDir       string   // working directory where binary is executed from
	runAs         string   // the unix user the process is run as
	outputPath    string   // where stdout/stderr of process is streamed to
	env           []string // extra "KEY=VALUE" environment entries set on the process
	leavePrevious bool     // skip terminating old process on upgrade (app handles its own rollover)

	status apigen.RunnerStatus

	stopping atomic.Bool
}

const (
	osProcessMinBackoff      = 1 * time.Second
	osProcessMaxBackoff      = 60 * time.Second
	osProcessStableRunWindow = 15 * time.Second
)

// reAttachOSProcessRunner attaches to existing process runner
func reAttachOSProcessRunner(store storage.OperatorStore, deploymentConfig apigen.DeploymentConfig, runnerStatus apigen.RunnerStatus) *osProcessRunner {
	ctx, cancel := context.WithCancel(context.Background())
	// todo: potential gap if the deploymentConfig has changed then these resolutions could be different from the version actually still running
	runAs := resolveRunAs(osProcessRunAs(&deploymentConfig))
	workDir := resolveWorkingDir(osProcessWorkingDir(&deploymentConfig), runAs)
	r := &osProcessRunner{
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
		store:         store,
		deploymentID:  deploymentConfig.ID,
		workDir:       workDir,
		runAs:         runAs,
		outputPath:    apigen.RunOutputFile(deploymentConfig.ID, runnerStatus.DeploymentConfigVersion),
		env:           osProcessEnv(&deploymentConfig),
		leavePrevious: osProcessStrategy(&deploymentConfig) == "leavePrevious",
		status:        runnerStatus,
	}
	go r.run()
	return r
}

// newOSProcessRunner upgrades the runner to the prepared config version and starts process runner
func newOSProcessRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *osProcessRunner {
	ctx, cancel := context.WithCancel(context.Background())

	runAs := resolveRunAs(osProcessRunAs(dep))
	workDir := resolveWorkingDir(osProcessWorkingDir(dep), runAs)

	configVersion := preparerStatus.DeploymentConfigVersion

	r := &osProcessRunner{
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
		store:         store,
		deploymentID:  dep.ID,
		workDir:       workDir,
		runAs:         runAs,
		outputPath:    apigen.RunOutputFile(dep.ID, configVersion),
		env:           osProcessEnv(dep),
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

	// hadProcess tracks whether a process has previously run (spawned or
	// adopted). The next successful spawn after a prior process counts as a
	// restart — this ensures adopted-process-death → respawn is counted and
	// that the increment is persisted atomically with the RUNNING status.
	hadProcess := false

	// If we were constructed to adopt an existing PID (via reAttachOSProcessRunner),
	// the first iteration polls that process rather than spawning a new one.
	// On exit we drop through to the normal spawn loop.
	adoptPid := int(r.status.RunningPid)
	if adoptPid > 0 {
		slog.InfoContext(r.ctx, "adopting existing process", "pid", adoptPid, "log", r.outputPath)
		r.monitorAdoptedProcess(adoptPid)
		hadProcess = true
		if !r.stopping.Load() {
			r.updateStatus(apigen.RunningStatus_CRASHED, int32(adoptPid))
		}
	}

	for {
		if r.stopping.Load() {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		if hadProcess {
			r.status.NumberOfRestarts++
		}
		hadProcess = true
		r.status.LastRestartAt = time.Now()
		// Resolve ${s:name}/${c:name} references at spawn time so values are not
		// persisted/logged and updates are picked up on respawn. Any unresolved
		// reference fails the spawn closed.
		env, err := resolveEnv(r.env)
		if err != nil {
			slog.ErrorContext(r.ctx, "resolving env references failed", "err", err)
			writeSpawnError(r.outputPath, err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}
		pid, err := spawnDaemon(r.status.RunningArtifact, r.workDir, r.outputPath, r.runAs, env)
		if err != nil {
			slog.ErrorContext(r.ctx, "spawning daemon failed", "err", err, "bin", r.status.RunningArtifact, "workDir", r.workDir, "runAs", r.runAs)
			writeSpawnError(r.outputPath, err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

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
	}
}

// writeSpawnError records a failed spawn in the deployment's output file so the
// reason (e.g. "permission denied") is visible in the run logs, not just the
// server log. spawnDaemon truncates the output file on each attempt, so on
// failure it is empty and this line stands alone. Best effort: a failure to
// write is itself only logged.
func writeSpawnError(outputPath string, spawnErr error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		slog.Error("creating run log dir for spawn error failed", "path", outputPath, "err", err)
		return
	}
	f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		slog.Error("writing spawn error to output file failed", "path", outputPath, "err", err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "Error spawning process: %v\n", spawnErr); err != nil {
		slog.Error("writing spawn error to output file failed", "path", outputPath, "err", err)
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
					// TODO: giving up here falls through to the respawn loop
					// without a distinct status write — the transition looks
					// like RUNNING(old pid) → RUNNING(new pid) in history,
					// hiding the fact that adoption was abandoned. Consider
					// writing CRASHED (or a dedicated state) before returning.
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
	// RunningPid is read concurrently by Stop() via atomic.LoadInt32, so
	// writes must go through atomic.StoreInt32 to avoid a data race.
	atomic.StoreInt32(&r.status.RunningPid, pid)
	r.writeStatus()
}

func (r *osProcessRunner) writeStatus() {
	r.store.MustWriteDeploymentStatus(r.deploymentID, func(s *apigen.DeploymentStatus) bool {
		if !s.Runner.IsZero() && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded runner")
			return false
		}
		s.BumpUpdatedAt()
		s.DeploymentID = r.deploymentID
		s.Runner = r.status
		return true
	})
}

// --- helpers ---

func resolveWorkingDir(dir, runAs string) string {
	if dir != "" {
		return dir
	}
	if runAs != "" {
		u, err := user.Lookup(runAs)
		if err != nil {
			slog.Error(fmt.Sprintf("resolving working dir: looking up user %v: %v", u, err))
			return ""
		}
		return u.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("resolving home dir:", "err", err)
		return ""
	}
	return home
}

// resolveRunAs returns the OS user to run the deployment process as. It
// defaults to "ubuntu" when the deployment does not specify a user.
func resolveRunAs(configRunAs string) string {
	if configRunAs != "" {
		return configRunAs
	}
	return "ubuntu"
}

func osProcessWorkingDir(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec.Runner.OsProcess.IsZero() {
		return ""
	}
	return dep.Spec.Runner.OsProcess.WorkingDir
}

func osProcessRunAs(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec.Runner.OsProcess.IsZero() {
		return ""
	}
	return dep.Spec.Runner.OsProcess.RunAs
}

func osProcessStrategy(dep *apigen.DeploymentConfig) string {
	if dep == nil || dep.Spec.Runner.OsProcess.IsZero() {
		return ""
	}
	return dep.Spec.Runner.OsProcess.Strategy
}

// osProcessEnv flattens the configured env vars into "KEY=VALUE" entries to be
// applied on the spawned process.
func osProcessEnv(dep *apigen.DeploymentConfig) []string {
	if dep == nil || dep.Spec.Runner.OsProcess.IsZero() {
		return nil
	}
	cfg := dep.Spec.Runner.OsProcess.Env
	if len(cfg) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg))
	for _, e := range cfg {
		if e == nil {
			continue
		}
		out = append(out, e.Key+"="+e.Value)
	}
	return out
}
