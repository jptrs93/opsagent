package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// systemdRunner manages a deployment via a systemd unit. On creation it
// installs the prepared artifact into dep.Spec.Runner.Systemd.BinPath via
// atomic rename and asks systemd to restart the unit, then polls
// `systemctl is-active` to drive lifecycle state. If the installed binary
// already matches the prepared artifact (the common opsagent-restart case)
// the install and restart are skipped — systemd owns process-level restarts.
//
// Unlike osProcessRunner, systemdRunner does not implement its own backoff:
// systemd's `Restart=` directives already handle that.
type systemdRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store storage.OperatorStore
	id    *apigen.DeploymentIdentifier
	seqNo int32

	unit         string
	binPath      string
	artifactPath string

	stopping atomic.Bool

	restartsMu  sync.Mutex
	restarts    int32
	lastRestart time.Time
}

func newSystemdRunner(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, artifact string, prev *apigen.RunnerStatus) *systemdRunner {
	ctx, cancel := context.WithCancel(parentCtx)
	sys := dep.Spec.Runner.Systemd

	r := &systemdRunner{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		store:        store,
		id:           dep.ID,
		seqNo:        dep.SeqNo,
		unit:         sys.Name,
		binPath:      sys.BinPath,
		artifactPath: artifact,
	}

	if prev != nil {
		r.restarts = prev.NumberOfRestarts
		r.lastRestart = prev.LastRestartAt
	}

	go r.run()
	return r
}

func (r *systemdRunner) SeqNo() int32 { return r.seqNo }

func (r *systemdRunner) Stop() {
	if !r.stopping.CompareAndSwap(false, true) {
		<-r.done
		return
	}
	r.cancel()
	if err := systemctl(context.Background(), "stop", r.unit); err != nil {
		slog.WarnContext(r.ctx, "systemctl stop failed", "unit", r.unit, "err", err)
	}
	r.writeStatus(apigen.RunningStatus_STOPPED, 0)
	<-r.done
}

func (r *systemdRunner) run() {
	defer close(r.done)

	alreadyInstalled, err := binMatchesArtifact(r.binPath, r.artifactPath)
	if err != nil {
		slog.WarnContext(r.ctx, "checking installed bin failed; will reinstall", "err", err, "bin", r.binPath)
	}
	if !alreadyInstalled {
		r.writeInitialStarting()
		if err := atomicInstall(r.artifactPath, r.binPath); err != nil {
			slog.ErrorContext(r.ctx, "installing artifact failed", "err", err, "dst", r.binPath, "src", r.artifactPath)
			r.writeStatus(apigen.RunningStatus_CRASHED, 0)
			return
		}
		slog.InfoContext(r.ctx, "restarting systemd unit", "unit", r.unit)
		if err := systemctl(r.ctx, "restart", r.unit); err != nil {
			slog.ErrorContext(r.ctx, "systemctl restart failed", "err", err, "unit", r.unit)
			r.writeStatus(apigen.RunningStatus_CRASHED, 0)
			return
		}
	} else {
		slog.InfoContext(r.ctx, "systemd bin already matches artifact; entering monitor mode", "unit", r.unit)
	}

	r.monitor()
}

// monitor polls `systemctl is-active` every 2 seconds and writes lifecycle
// transitions. The loop only exits on Stop or context cancellation —
// transient CRASHED/STOPPED states are reported but the monitor keeps
// running so that systemd's own `Restart=` directive can recover the unit
// and the next tick picks up the new RUNNING state.
func (r *systemdRunner) monitor() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastStatus apigen.RunningStatus
	for {
		if r.stopping.Load() {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			active, err := systemctlIsActive(r.ctx, r.unit)
			if err != nil {
				slog.WarnContext(r.ctx, "systemctl is-active failed", "unit", r.unit, "err", err)
				continue
			}
			status := mapActiveState(active)
			if status == lastStatus {
				continue
			}
			lastStatus = status
			pid, _ := systemctlMainPID(r.ctx, r.unit)
			r.writeStatus(status, pid)
		}
	}
}

// writeInitialStarting seeds the first RunnerStatus for a fresh install,
// bumping NumberOfRestarts when the same dep.SeqNo was previously observed.
func (r *systemdRunner) writeInitialStarting() {
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
			RunningArtifact:  r.artifactPath,
			NumberOfRestarts: restarts,
			LastRestartAt:    lastRestart,
		}
	})
}

func (r *systemdRunner) writeStatus(status apigen.RunningStatus, pid int) {
	r.restartsMu.Lock()
	restarts, lastRestart := r.restarts, r.lastRestart
	r.restartsMu.Unlock()

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
			RunningArtifact:  r.artifactPath,
			NumberOfRestarts: restarts,
			LastRestartAt:    lastRestart,
		}
	})
}

// --- systemctl helpers ---

func binMatchesArtifact(binPath, artifactPath string) (bool, error) {
	if binPath == "" || artifactPath == "" {
		return false, nil
	}
	binInfo, err := os.Stat(binPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	artInfo, err := os.Stat(artifactPath)
	if err != nil {
		return false, err
	}
	return os.SameFile(binInfo, artInfo), nil
}

// atomicInstall copies src to a sibling temp file next to dst and renames it
// into place. Renaming is atomic and avoids ETXTBSY on a running binary.
func atomicInstall(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating bin dir: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening artifact: %w", err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".new-*")
	if err != nil {
		return fmt.Errorf("creating temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("copying artifact: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		cleanup()
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

func systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func systemctlIsActive(ctx context.Context, unit string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state != "" {
		return state, nil
	}
	if err != nil {
		return "", err
	}
	return state, nil
}

func systemctlMainPID(ctx context.Context, unit string) (int, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=MainPID", "--value").Output()
	if err != nil {
		return 0, err
	}
	raw := strings.TrimSpace(string(out))
	var pid int
	if _, err := fmt.Sscanf(raw, "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func mapActiveState(state string) apigen.RunningStatus {
	switch state {
	case "active", "reloading":
		return apigen.RunningStatus_RUNNING
	case "activating":
		return apigen.RunningStatus_STARTING
	case "deactivating", "inactive":
		return apigen.RunningStatus_STOPPED
	case "failed":
		return apigen.RunningStatus_CRASHED
	default:
		return apigen.RunningStatus_CRASHED
	}
}
