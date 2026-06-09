package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Containerd is the process-wide containerd client, wired by the bootstrap. It
// is shared with the container image preparer.
var Containerd *ctrd.Client

// containerRunner owns the create/start/monitor/respawn/backoff lifecycle of a
// single deployment's container, mirroring osProcessRunner but driving
// containerd instead of fork/exec. One container per deployment, keyed by a
// deterministic id so reattach can find it after an opendeploy restart.
type containerRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store        storage.OperatorStore
	deploymentID int32
	containerID  string

	// derived from the deployment config version; not part of RunnerStatus.
	user           string
	env            []string // "KEY=VALUE" entries; ${s:name}/${c:name} refs resolved at start
	command        []string // argv override; empty = image default
	cwd            string   // process cwd; empty = image default
	mounts         []ctrd.Mount
	dataVolumeHost string // host dir to create+chown for the default data volume ("" = disabled)
	dataVolumeUser string // user the data volume should be owned by

	status apigen.RunnerStatus

	stopping atomic.Bool

	taskMu sync.Mutex
	task   *ctrd.Task
}

// containerID is the deterministic containerd id for a deployment.
func containerID(deploymentID int32) string {
	return fmt.Sprintf("opendeploy-%d", deploymentID)
}

func newContainerRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, dep)
	r.status = apigen.RunnerStatus{
		DeploymentConfigVersion: configVersion,
		RunningArtifact:         preparerStatus.Artifact,
		Status:                  apigen.RunningStatus_STARTING,
		LastRestartAt:           time.Now(),
	}
	r.writeStatus()
	go r.run()
	return r
}

func reAttachContainerRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	r := buildContainerRunner(ctx, cancel, store, dep)
	r.status = prev
	go r.run()
	return r
}

func buildContainerRunner(ctx context.Context, cancel context.CancelFunc, store storage.OperatorStore, dep *apigen.DeploymentConfig) *containerRunner {
	cfg := dep.Spec.Runner.Container
	r := &containerRunner{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		store:        store,
		deploymentID: dep.ID,
		containerID:  containerID(dep.ID),
		user:         cfg.User,
		env:          containerEnv(dep),
		command:      cfg.Command,
		cwd:          cfg.WorkingDir,
	}
	r.mounts, r.dataVolumeHost = containerMounts(dep)
	r.dataVolumeUser = cfg.User
	return r
}

func (r *containerRunner) Version() int32 { return r.status.DeploymentConfigVersion }

func (r *containerRunner) Stop() {
	if !r.stopping.CompareAndSwap(false, true) {
		<-r.done
		return
	}

	task := r.getTask()
	if task != nil {
		// Graceful: SIGTERM, give the container time to exit (the run loop's
		// monitor wakes on real exit and writes STOPPED), then SIGKILL.
		if err := task.Kill(context.Background(), syscall.SIGTERM); err != nil {
			slog.Warn("sending SIGTERM to container failed", "id", r.containerID, "err", err)
		}
		select {
		case <-r.done:
			r.deleteTask(task)
			return
		case <-time.After(3 * time.Second):
		}
		if err := task.Kill(context.Background(), syscall.SIGKILL); err != nil {
			slog.Warn("sending SIGKILL to container failed", "id", r.containerID, "err", err)
		}
	}
	// Break out of the monitor/backoff loop and wait for it to finish, then
	// remove the container.
	r.cancel()
	<-r.done
	if task != nil {
		r.deleteTask(task)
	}
}

func (r *containerRunner) run() {
	defer close(r.done)

	crashCount := 0
	hadProcess := false

	// Reattach: if a container for this deployment is still running, adopt it.
	if !r.status.IsZero() && (r.status.RunningPid != 0 || r.status.Status == apigen.RunningStatus_RUNNING) {
		if task, err := Containerd.LoadTask(r.ctx, r.containerID); err == nil {
			slog.InfoContext(r.ctx, "adopting running container", "id", r.containerID, "pid", task.Pid())
			r.setTask(task)
			r.monitorTask(task)
			hadProcess = true
			if !r.stopping.Load() {
				r.updateStatus(apigen.RunningStatus_CRASHED, int32(task.Pid()))
			}
		} else {
			slog.InfoContext(r.ctx, "no running container to adopt, spawning fresh", "id", r.containerID)
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

		// Resolve ${s:name}/${c:name} references at spawn time (values not
		// persisted/logged; updates picked up on respawn).
		env, err := resolveEnv(r.env)
		if err != nil {
			slog.ErrorContext(r.ctx, "resolving env references failed", "err", err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		r.ensureDataVolume()

		spec := ctrd.ContainerSpec{
			ID:     r.containerID,
			Image:  r.status.RunningArtifact,
			User:   r.user,
			Env:    env,
			Args:   r.command,
			Cwd:    r.cwd,
			Mounts: r.mounts,
		}
		task, err := Containerd.RunTask(r.ctx, spec)
		if err != nil {
			slog.ErrorContext(r.ctx, "starting container failed", "err", err, "image", spec.Image, "id", r.containerID)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		r.setTask(task)
		slog.InfoContext(r.ctx, "container started", "id", r.containerID, "pid", task.Pid(), "image", spec.Image)
		r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
		startedAt := time.Now()

		r.monitorTask(task)

		if r.stopping.Load() {
			// Stop() owns the kill + delete; just record STOPPED and exit.
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		// Crash: stability reset, record CRASHED, clean up, back off, respawn.
		if time.Since(startedAt) >= osProcessStableRunWindow {
			crashCount = 0
		}
		crashCount++
		r.updateStatus(apigen.RunningStatus_CRASHED, int32(task.Pid()))
		r.deleteTask(task)

		if !r.sleepBackoff(crashCount) {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}
	}
}

// monitorTask blocks until the task exits or the runner context is cancelled.
func (r *containerRunner) monitorTask(task *ctrd.Task) {
	exitCh, err := task.Wait(r.ctx)
	if err != nil {
		slog.WarnContext(r.ctx, "waiting on container task failed", "id", r.containerID, "err", err)
		return
	}
	select {
	case <-exitCh:
	case <-r.ctx.Done():
	}
}

func (r *containerRunner) sleepBackoff(crashCount int) bool {
	delay := computeOSProcessBackoff(crashCount)
	slog.InfoContext(r.ctx, "backoff sleep before container respawn", "delay", delay, "crashes", crashCount)
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// ensureDataVolume creates and chowns the default data-volume host directory so
// the in-container user can write to it. Best effort: a failure is logged but
// does not block the spawn (the bind mount still works for a root container).
func (r *containerRunner) ensureDataVolume() {
	if r.dataVolumeHost == "" {
		return
	}
	if err := os.MkdirAll(r.dataVolumeHost, 0o755); err != nil {
		slog.WarnContext(r.ctx, "creating data volume dir failed", "path", r.dataVolumeHost, "err", err)
		return
	}
	if err := chownToUser(r.dataVolumeHost, r.dataVolumeUser); err != nil {
		slog.WarnContext(r.ctx, "chowning data volume dir failed (non-root container users may be unable to write)",
			"path", r.dataVolumeHost, "user", r.dataVolumeUser, "err", err)
	}
}

func (r *containerRunner) deleteTask(task *ctrd.Task) {
	if task == nil {
		return
	}
	if err := task.Delete(context.Background()); err != nil {
		slog.WarnContext(r.ctx, "deleting container failed", "id", r.containerID, "err", err)
	}
}

func (r *containerRunner) setTask(task *ctrd.Task) {
	r.taskMu.Lock()
	r.task = task
	r.taskMu.Unlock()
}

func (r *containerRunner) getTask() *ctrd.Task {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	return r.task
}

// --- state writes ---

func (r *containerRunner) updateStatus(status apigen.RunningStatus, pid int32) {
	r.status.Status = status
	r.status.RunningPid = pid
	r.writeStatus()
}

func (r *containerRunner) writeStatus() {
	r.store.MustWriteDeploymentStatus(r.deploymentID, func(s *apigen.DeploymentStatus) bool {
		if !s.Runner.IsZero() && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded container runner")
			return false
		}
		s.BumpUpdatedAt()
		s.DeploymentID = r.deploymentID
		s.Runner = r.status
		return true
	})
}

// --- config helpers ---

// containerEnv flattens the configured env vars into "KEY=VALUE" entries.
func containerEnv(dep *apigen.DeploymentConfig) []string {
	cfg := dep.Spec.Runner.Container.Env
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

// containerMounts builds the container's bind mounts: the default per-deployment
// data volume (unless disabled) followed by any configured mounts. It also
// returns the default volume's host path (empty when disabled) so the runner can
// create + chown it at spawn time.
func containerMounts(dep *apigen.DeploymentConfig) ([]ctrd.Mount, string) {
	cfg := dep.Spec.Runner.Container
	var mounts []ctrd.Mount
	var dataHost string
	if !cfg.DisableDataVolume {
		dataHost = defaultVolumeHostDir(dep.ID)
		mounts = append(mounts, ctrd.Mount{
			Source: dataHost,
			Dest:   defaultVolumeDest(cfg.User, cfg.DataMountPath),
		})
	}
	for _, m := range cfg.Mounts {
		if m == nil {
			continue
		}
		mounts = append(mounts, ctrd.Mount{Source: m.Host, Dest: m.Container, ReadOnly: m.Readonly})
	}
	return mounts, dataHost
}

// defaultVolumeHostDir is the opendeploy-owned host directory bind-mounted as the
// container's default data volume. A sibling of the data dir (world-traversable,
// 0755), like release artifacts, so the in-container user can reach it.
func defaultVolumeHostDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.VolumesDir, strconv.Itoa(int(deploymentID)), "data")
}

// defaultVolumeDest is the in-container mount point for the default data volume:
// the explicit override if set, else /data for a root container, else
// /home/<user>/data.
func defaultVolumeDest(usr, override string) string {
	if override != "" {
		return override
	}
	if usr == "" || usr == "root" || usr == "0" {
		return "/data"
	}
	name := usr
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return "/home/" + name + "/data"
}

// chownToUser chowns path to the host uid/gid for usr. usr may be "", "root", a
// numeric "uid[:gid]", or a name resolvable on the host. Names that exist only
// inside the image cannot be resolved host-side and return an error (handled
// best-effort by the caller).
func chownToUser(path, usr string) error {
	uid, gid := 0, 0
	if usr != "" && usr != "root" {
		name, gname := usr, ""
		if i := strings.IndexByte(usr, ':'); i >= 0 {
			name, gname = usr[:i], usr[i+1:]
		}
		if n, err := strconv.Atoi(name); err == nil {
			uid, gid = n, n
		} else if u, err := user.Lookup(name); err == nil {
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
		} else {
			return err
		}
		if gname != "" {
			if n, err := strconv.Atoi(gname); err == nil {
				gid = n
			} else if g, err := user.LookupGroup(gname); err == nil {
				gid, _ = strconv.Atoi(g.Gid)
			}
		}
	}
	return os.Chown(path, uid, gid)
}
