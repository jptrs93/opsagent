package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Containerd is the process-wide containerd client, wired by the bootstrap. It
// is shared with the container image preparer.
var Containerd *ctrd.Client

const (
	containerMinBackoff      = 1 * time.Second
	containerMaxBackoff      = 30 * time.Second
	containerStableRunWindow = 15 * time.Second

	containerReadinessDefaultTimeout = 10 * time.Minute
	containerReadinessEnvKey         = "OPENDEPLOY_READINESS_SOCK_PATH"
	containerReadinessContainerDir   = "/run/opendeploy"
	containerReadinessSocketName     = "readiness.sock"
	containerReadinessContainerPath  = containerReadinessContainerDir + "/" + containerReadinessSocketName
)

// containerRunner owns the create/start/monitor/respawn/backoff lifecycle of a
// single deployment config version's container. The deterministic id includes
// the version so reattach can find the exact task after an opendeploy restart.
type containerRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store          storage.OperatorStore
	deploymentID   int32
	deploymentName string
	containerID    string

	// derived from the deployment config version; not part of RunnerStatus.
	user           string
	envVars        map[string]*apigen.EnvVarValue // resolved to "KEY=VALUE" entries at start
	command        []string                       // argv override; empty = image default
	cwd            string                         // process cwd; empty = image default
	mounts         []ctrd.Mount
	configVersion  int32
	dataVolumeHost string // host dir to create+chown for the default data volume ("" = disabled)
	dataVolumeUser string // user the data volume should be owned by
	readiness      *readinessConfig
	startupMode    containerStartupMode

	status apigen.RunnerStatus

	stopping atomic.Bool
	publish  atomic.Bool

	taskMu sync.Mutex
	task   *ctrd.Task

	readyOnce sync.Once
	readyCh   chan error
}

type readinessConfig struct {
	timeout time.Duration
}

type containerStartupMode int

const (
	containerStartupStartFresh containerStartupMode = iota
	containerStartupReattachRunning
	containerStartupReattachStopped
)

// containerID is the deterministic containerd id for a deployment config version.
func containerID(deploymentID int32, configVersion int32) string {
	return fmt.Sprintf("opendeploy-%d-v%d", deploymentID, configVersion)
}

func newContainerRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, dep, configVersion)
	r.publish.Store(true)
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

func newRolloverContainerRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, dep, configVersion)
	r.readiness = &readinessConfig{timeout: containerReadinessTimeout(dep.Spec.Runner.Container.ReadinessSignal)}
	r.readyCh = make(chan error, 1)
	r.status = apigen.RunnerStatus{
		DeploymentConfigVersion: configVersion,
		RunningArtifact:         preparerStatus.Artifact,
		Status:                  apigen.RunningStatus_STARTING,
		LastRestartAt:           time.Now(),
	}
	go r.run()
	return r
}

func reAttachContainerRunner(store storage.OperatorStore, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus, mode containerStartupMode) *containerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	r := buildContainerRunner(ctx, cancel, store, dep, prev.DeploymentConfigVersion)
	r.publish.Store(true)
	r.status = prev
	r.startupMode = mode
	go r.run()
	return r
}

func containerReadinessTimeout(sig *apigen.ContainerReadinessSignal) time.Duration {
	if sig != nil && sig.TimeoutSeconds > 0 {
		return time.Duration(sig.TimeoutSeconds) * time.Second
	}
	return containerReadinessDefaultTimeout
}

func buildContainerRunner(ctx context.Context, cancel context.CancelFunc, store storage.OperatorStore, dep *apigen.DeploymentConfig, configVersion int32) *containerRunner {
	cfg := dep.Spec.Runner.Container
	r := &containerRunner{
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		store:          store,
		deploymentID:   dep.ID,
		deploymentName: containerDeploymentName(dep),
		containerID:    containerID(dep.ID, configVersion),
		configVersion:  configVersion,
		user:           cfg.User,
		envVars:        cfg.EnvVars,
		command:        cfg.Command,
		cwd:            cfg.WorkingDir,
	}
	r.mounts, r.dataVolumeHost = containerMounts(dep)
	r.dataVolumeUser = cfg.User
	return r
}

func containerDeploymentName(dep *apigen.DeploymentConfig) string {
	if dep == nil {
		return "<nil>"
	}
	if !dep.ConfigID.IsZero() {
		return fmt.Sprintf("%d:%s:%s", dep.ConfigID.SpaceID, dep.ConfigID.Machine, dep.ConfigID.Name)
	}
	return fmt.Sprintf("id=%d", dep.ID)
}

func (r *containerRunner) Version() int32 { return r.status.DeploymentConfigVersion }

func (r *containerRunner) WaitReady() error {
	if r.readyCh == nil {
		return nil
	}
	err, ok := <-r.readyCh
	if !ok {
		return nil
	}
	return err
}

func (r *containerRunner) Promote() {
	r.publish.Store(true)
	r.writeStatus()
}

func (r *containerRunner) Stop() {
	if !r.stopping.CompareAndSwap(false, true) {
		<-r.done
		return
	}
	r.notifyReady(context.Canceled)

	task := r.getTask()
	if task != nil {
		r.logContainerEvent("stop", r.currentRunNumber(), r.mounts)
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

	// Reattach: reconcile any existing containerd task for this deterministic id.
	if r.startupMode == containerStartupReattachRunning || r.startupMode == containerStartupReattachStopped {
		if task, err := Containerd.LoadTask(r.ctx, r.containerID); err == nil {
			r.logContainerEvent("re-attach", r.currentRunNumber(), r.mounts)
			r.setTask(task)
			if r.startupMode == containerStartupReattachStopped {
				r.stopAdoptedTask(task)
				if r.shouldPublishStopped() {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				}
				return
			}
			r.monitorTask(task)
			hadProcess = true
			if !r.stopping.Load() {
				r.updateStatus(apigen.RunningStatus_CRASHED, int32(task.Pid()))
			}
		} else {
			r.logContainerEvent("re-attach-miss", r.currentRunNumber(), r.mounts)
			if r.startupMode == containerStartupReattachStopped {
				if r.shouldPublishStopped() {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				}
				return
			}
			hadProcess = r.status.RunningPid != 0 ||
				r.status.Status == apigen.RunningStatus_RUNNING ||
				r.status.Status == apigen.RunningStatus_STARTING
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

		// Resolve typed env references at spawn time (values not persisted/logged;
		// updates picked up on respawn).
		env, err := resolveEnv(r.envVars)
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
		runNumber := r.status.NumberOfRestarts + 1
		outputDir := apigen.RunOutputDeploymentDir(r.deploymentID)
		if err := os.MkdirAll(outputDir, 0o750); err != nil {
			slog.ErrorContext(r.ctx, "creating run log dir failed", "err", err, "path", outputDir)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		mounts := r.mounts
		readinessActive := r.readiness != nil && !r.publish.Load()
		var ready <-chan error
		var closeReady func()
		if readinessActive {
			listener, err := r.startReadinessListener(runNumber)
			if err != nil {
				slog.ErrorContext(r.ctx, "starting readiness listener failed", "err", err)
				r.notifyReady(fmt.Errorf("starting readiness listener: %w", err))
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
				return
			}
			ready = listener.ready
			closeReady = listener.close
			mounts = append(append([]ctrd.Mount{}, mounts...), ctrd.Mount{Source: listener.dir, Dest: containerReadinessContainerDir})
			env = append(env, containerReadinessEnvKey+"="+containerReadinessContainerPath)
		}

		spec := ctrd.ContainerSpec{
			ID:            r.containerID,
			Image:         r.status.RunningArtifact,
			User:          r.user,
			Env:           env,
			Args:          r.command,
			Cwd:           r.cwd,
			Mounts:        mounts,
			Output:        outputDir,
			OutputVersion: r.status.DeploymentConfigVersion,
			OutputRun:     runNumber,
		}
		task, err := Containerd.RunTask(r.ctx, spec)
		if err != nil {
			if closeReady != nil {
				closeReady()
			}
			slog.ErrorContext(r.ctx, "starting container failed", "err", err, "image", spec.Image, "id", r.containerID)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			if readinessActive {
				r.notifyReady(fmt.Errorf("starting container: %w", err))
				return
			}
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		r.setTask(task)
		r.logContainerEvent("start", runNumber, mounts)
		r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
		startedAt := time.Now()

		exitDone := make(chan struct{})
		go func() {
			r.monitorTask(task)
			close(exitDone)
		}()

		if readinessActive {
			readyOK, taskExited := r.waitForReadiness(ready, closeReady, exitDone)
			if !readyOK {
				if !taskExited {
					_ = task.Kill(context.Background(), syscall.SIGTERM)
				}
				r.deleteTask(task)
				return
			}
		}

		<-exitDone
		if closeReady != nil {
			closeReady()
		}

		if r.stopping.Load() {
			// Stop() owns the kill + delete; just record STOPPED and exit.
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}

		// Crash: stability reset, record CRASHED, clean up, back off, respawn.
		if time.Since(startedAt) >= containerStableRunWindow {
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

func (r *containerRunner) stopAdoptedTask(task *ctrd.Task) {
	if task == nil {
		return
	}
	r.logContainerEvent("stop", r.currentRunNumber(), r.mounts)
	if err := task.Kill(context.Background(), syscall.SIGTERM); err != nil {
		slog.WarnContext(r.ctx, "sending SIGTERM to adopted container failed", "id", r.containerID, "err", err)
	}
	exitDone := make(chan struct{})
	go func() {
		r.monitorTask(task)
		close(exitDone)
	}()
	select {
	case <-exitDone:
	case <-time.After(3 * time.Second):
		if err := task.Kill(context.Background(), syscall.SIGKILL); err != nil {
			slog.WarnContext(r.ctx, "sending SIGKILL to adopted container failed", "id", r.containerID, "err", err)
		}
		select {
		case <-exitDone:
		case <-time.After(5 * time.Second):
		}
	}
	r.deleteTask(task)
	r.setTask(nil)
}

func (r *containerRunner) shouldPublishStopped() bool {
	switch r.status.Status {
	case apigen.RunningStatus_STOPPED, apigen.RunningStatus_NO_DEPLOYMENT:
		return r.status.RunningPid != 0
	default:
		return true
	}
}

func (r *containerRunner) logContainerEvent(action string, runNumber int32, mounts []ctrd.Mount) {
	counts := countEnvVars(r.envVars)
	slog.Info(fmt.Sprintf(
		"container %s deployment=%q config_version=%d run_number=%d mount_paths=%q env_plain_count=%d env_config_count=%d env_secret_count=%d env_asset_count=%d",
		action,
		r.deploymentName,
		r.configVersion,
		runNumber,
		formatMountPaths(mounts),
		counts.plain,
		counts.config,
		counts.secret,
		counts.asset,
	))
}

func (r *containerRunner) currentRunNumber() int32 {
	return r.status.NumberOfRestarts + 1
}

type envVarCounts struct {
	plain  int
	config int
	secret int
	asset  int
}

func countEnvVars(env map[string]*apigen.EnvVarValue) envVarCounts {
	var counts envVarCounts
	for _, value := range env {
		if value == nil {
			continue
		}
		if value.Value != nil {
			counts.plain++
		}
		if value.ConfigID != nil {
			counts.config++
		}
		if value.SecretID != nil {
			counts.secret++
		}
		if value.Asset != "" {
			counts.asset++
		}
	}
	return counts
}

func formatMountPaths(mounts []ctrd.Mount) string {
	if len(mounts) == 0 {
		return "-"
	}
	paths := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mode := "rw"
		if mount.ReadOnly {
			mode = "ro"
		}
		paths = append(paths, fmt.Sprintf("%s->%s(%s)", mount.Source, mount.Dest, mode))
	}
	return strings.Join(paths, ";")
}

func (r *containerRunner) sleepBackoff(crashCount int) bool {
	delay := computeContainerBackoff(crashCount)
	slog.InfoContext(r.ctx, "backoff sleep before container respawn", "delay", delay, "crashes", crashCount)
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

type readinessListener struct {
	dir   string
	ready <-chan error
	close func()
}

func (r *containerRunner) startReadinessListener(runNumber int32) (*readinessListener, error) {
	dir := filepath.Join(ainit.StaticConfig.DataDir, "readiness", strconv.Itoa(int(r.deploymentID)), strconv.Itoa(int(r.status.DeploymentConfigVersion)), strconv.Itoa(int(runNumber)))
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		return nil, err
	}
	sockPath := filepath.Join(dir, containerReadinessSocketName)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		defer close(ready)
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-r.ctx.Done():
					ready <- r.ctx.Err()
				default:
					if !errors.Is(err, net.ErrClosed) {
						ready <- err
					}
				}
				return
			}
			if readinessMessage(conn) {
				ready <- nil
				return
			}
		}
	}()
	return &readinessListener{
		dir:   dir,
		ready: ready,
		close: func() { _ = listener.Close() },
	}, nil
}

func readinessMessage(conn net.Conn) bool {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(buf[:n])) == "ready"
}

func (r *containerRunner) waitForReadiness(ready <-chan error, closeReady func(), exitDone <-chan struct{}) (bool, bool) {
	defer closeReady()
	timer := time.NewTimer(r.readiness.timeout)
	defer timer.Stop()
	select {
	case err, ok := <-ready:
		if !ok {
			err = fmt.Errorf("readiness listener closed before signal")
		}
		if err != nil {
			r.notifyReady(fmt.Errorf("readiness signal failed: %w", err))
			return false, false
		}
		slog.InfoContext(r.ctx, "container readiness signal received", "id", r.containerID)
		r.notifyReady(nil)
		return true, false
	case <-exitDone:
		err := fmt.Errorf("container exited before readiness signal")
		r.notifyReady(err)
		return false, true
	case <-timer.C:
		err := fmt.Errorf("timed out after %s waiting for readiness signal", r.readiness.timeout)
		r.notifyReady(err)
		return false, false
	case <-r.ctx.Done():
		r.notifyReady(r.ctx.Err())
		return false, false
	}
}

func (r *containerRunner) notifyReady(err error) {
	if r.readyCh == nil {
		return
	}
	r.readyOnce.Do(func() {
		r.readyCh <- err
		close(r.readyCh)
	})
}

func computeContainerBackoff(crashCount int) time.Duration {
	if crashCount <= 1 {
		return containerMinBackoff
	}
	delay := containerMinBackoff
	for i := 1; i < crashCount; i++ {
		delay *= 2
		if delay >= containerMaxBackoff {
			return containerMaxBackoff
		}
	}
	return delay
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
	if !r.publish.Load() {
		return
	}
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
	for _, m := range cfg.AssetMounts {
		if m == nil {
			continue
		}
		hostPath := preparer.AssetCachePath(m.AssetID, m.Version)
		mounts = append(mounts, ctrd.Mount{Source: hostPath, Dest: m.Path, ReadOnly: true})
	}
	implicitMounted := map[string]bool{}
	envKeys := make([]string, 0, len(cfg.EnvVars))
	for key := range cfg.EnvVars {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		value := cfg.EnvVars[key]
		if value == nil || value.Asset == "" || value.AssetID <= 0 || value.Version <= 0 {
			continue
		}
		dest := implicitAssetContainerPath(value.AssetID, value.Version)
		if implicitMounted[dest] {
			continue
		}
		implicitMounted[dest] = true
		mounts = append(mounts, ctrd.Mount{Source: preparer.AssetCachePath(value.AssetID, value.Version), Dest: dest, ReadOnly: true})
	}
	return mounts, dataHost
}

// defaultVolumeHostDir is the opendeploy-owned host directory bind-mounted as the
// container's default data volume. A sibling of the data dir (world-traversable,
// 0755), like release artifacts, so the in-container user can reach it.
func defaultVolumeHostDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.VolumesDir, strconv.Itoa(int(deploymentID)), "default")
}

// defaultVolumeDest is the in-container mount point for the default data volume:
// the explicit override if set, else /var for a root container, else
// /home/<user>/var.
func defaultVolumeDest(usr, override string) string {
	if override != "" {
		return override
	}
	if usr == "" || usr == "root" || usr == "0" {
		return "/var"
	}
	name := usr
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return "/home/" + name + "/var"
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
