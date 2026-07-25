package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
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
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"golang.org/x/sys/unix"
)

const (
	containerMinBackoff      = 1 * time.Second
	containerMaxBackoff      = 1 * time.Hour
	containerStableRunWindow = 15 * time.Second

	containerReadinessDefaultTimeout = 10 * time.Minute
	containerReadinessEnvKey         = "OPENDEPLOY_READINESS_SOCK_PATH"
	containerReadinessContainerDir   = "/run/opendeploy"
	containerReadinessSocketName     = "readiness.sock"
	containerReadinessContainerPath  = containerReadinessContainerDir + "/" + containerReadinessSocketName
	containerDefaultDevShmSizeKB     = 64 * 1024
)

// containerRunner owns the create/start/monitor/respawn/backoff lifecycle of a
// single deployment config version's container. The deterministic id includes
// the version so reattach can find the exact task after an opendeploy restart.
type containerRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	store               storage.OperatorStore
	runtimeInputs       *runtimeinputs.RuntimeInputs
	scheduledInstanceID int32
	deploymentID        int32
	spaceID             int32
	deploymentName      string
	nodeID              int32
	containerID         string

	// derived from the deployment config version; not part of RunnerStatus.
	user           string
	envVars        map[string]*apigen.EnvVarValue // resolved to "KEY=VALUE" entries at start
	command        []string                       // argv override; empty = image default
	cwd            string                         // process cwd; empty = image default
	mounts         []ctrd.Mount
	devShmSizeKB   int64
	fileDescLimit  int64
	configVersion  int32
	latestVersion  int32
	dataVolumeHost string // host dir to create+chown for the default data volume ("" = disabled)
	dataVolumeUser string // user the data volume should be owned by
	readiness      *readinessConfig
	startupMode    containerStartupMode
	networking     apigen.NetworkingConfig

	status apigen.RunnerStatus

	stopping atomic.Bool
	publish  atomic.Bool

	// servingMu guards serving and serialises it against claiming the address,
	// so a Serve call cannot interleave with the start loop's own claim and
	// leave the route installed twice or not at all.
	servingMu sync.Mutex
	serving   bool

	taskMu sync.Mutex
	task   *ctrd.Task
	netMu  sync.Mutex
	net    *network.ContainerNet

	readyOnce       sync.Once
	readyCh         chan error
	artifactMissing chan struct{}
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

func newContainerRunner(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, inputs, instanceID, dep, configVersion)
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

func newRolloverContainerRunner(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, inputs, instanceID, dep, configVersion)
	r.readiness = &readinessConfig{timeout: containerReadinessTimeout(dep.Spec.Container().ReadinessSignal)}
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

func reAttachContainerRunner(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus, mode containerStartupMode) *containerRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	r := buildContainerRunner(ctx, cancel, store, inputs, instanceID, dep, prev.DeploymentConfigVersion)
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

func buildContainerRunner(ctx context.Context, cancel context.CancelFunc, store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, configVersion int32) *containerRunner {
	cfg := dep.Spec.Container().Runtime
	r := &containerRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		artifactMissing:     make(chan struct{}, 1),
		store:               store,
		runtimeInputs:       inputs,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
		spaceID:             dep.Identity.SpaceID,
		deploymentName:      containerDeploymentName(dep),
		nodeID:              dep.NodeID,
		containerID:         containerID(dep.ID, configVersion),
		configVersion:       configVersion,
		user:                cfg.User,
		envVars:             cfg.EnvVars,
		command:             cfg.OverrideCommand,
		cwd:                 cfg.OverrideWorkingDir,
		networking:          dep.Spec.Networking,
		latestVersion:       dep.Version,
	}
	r.mounts, r.dataVolumeHost = containerMounts(dep)
	r.devShmSizeKB = int64(cfg.DevShmSizeKb)
	r.fileDescLimit = int64(cfg.FileDescriptorLimit)
	r.dataVolumeUser = cfg.User
	return r
}

func containerDeploymentName(dep *apigen.DeploymentConfig) string {
	if dep == nil {
		return "<nil>"
	}
	if !dep.Identity.IsZero() {
		return fmt.Sprintf("%d:%d:%s", dep.Identity.SpaceID, dep.NodeID, dep.Identity.Name)
	}
	return fmt.Sprintf("id=%d", dep.ID)
}

func (r *containerRunner) Version() int32 { return r.status.DeploymentConfigVersion }

func (r *containerRunner) ArtifactMissing() <-chan struct{} { return r.artifactMissing }

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

// Serve claims the instance's inbound address for this placement. It is
// idempotent, and a no-op before the container network exists: the start loop
// makes the same claim once it has one, so whichever happens second wins.
func (r *containerRunner) Serve() error {
	r.servingMu.Lock()
	defer r.servingMu.Unlock()
	r.serving = true
	return r.claimInboundAddressLocked(r.getContainerNet())
}

// claimInboundAddressLocked installs the host route for the stable inbound
// address and takes over the deployment's published host ports. Caller must
// hold servingMu.
func (r *containerRunner) claimInboundAddressLocked(cn *network.ContainerNet) error {
	if !r.serving || cn == nil || !r.virtualNetwork() {
		return nil
	}
	// An older application run must not take the address or the host ports back
	// from the replacement that superseded it, even though both belong to the
	// same serving placement.
	if !r.usesLatestNetworkConfig() {
		return nil
	}
	old := network.Default.CurrentNet(r.deploymentID)
	if old != nil && old.ContainerID == cn.ContainerID {
		return nil
	}
	if err := network.Default.Activate(cn); err != nil {
		return err
	}
	if err := r.publishContainerNet(cn); err != nil {
		if old != nil {
			if rollbackErr := network.Default.Activate(old); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restoring previous inbound route: %w", rollbackErr))
			}
		}
		return err
	}
	return nil
}

// claimInboundAddress is the start loop's entry point: it claims the address
// only if this placement is already serving, and does nothing for a standby
// warming up behind another placement.
func (r *containerRunner) claimInboundAddress(cn *network.ContainerNet) error {
	r.servingMu.Lock()
	defer r.servingMu.Unlock()
	return r.claimInboundAddressLocked(cn)
}

// Promote is the rollover candidate's readiness handoff. A candidate is by
// construction the replacement on this node, so promotion both claims the
// address and starts publishing status.
func (r *containerRunner) Promote() error {
	r.servingMu.Lock()
	r.serving = true
	err := r.claimInboundAddressLocked(r.getContainerNet())
	r.servingMu.Unlock()
	if err != nil {
		return err
	}
	r.publish.Store(true)
	r.writeStatus()
	return nil
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
			slog.WarnContext(r.ctx, "sending SIGTERM to container failed", "containerID", r.containerID, "err", err)
		}
		select {
		case <-r.done:
			r.deleteTask(task)
			return
		case <-time.After(3 * time.Second):
		}
		if err := task.Kill(context.Background(), syscall.SIGKILL); err != nil {
			slog.WarnContext(r.ctx, "sending SIGKILL to container failed", "containerID", r.containerID, "err", err)
		}
	}
	// Break out of the monitor/backoff loop and wait for it to finish, then
	// remove the container.
	r.cancel()
	<-r.done
	if task != nil {
		r.deleteTask(task)
	}
	r.cleanupContainerNetState()
}

func (r *containerRunner) run() {
	defer close(r.done)

	crashCount := 0
	hadProcess := false

	// Reattach: reconcile any existing containerd task for this deterministic id.
	if r.startupMode == containerStartupReattachRunning || r.startupMode == containerStartupReattachStopped {
		if task, err := ctrd.Default.LoadTask(r.ctx, r.containerID); err == nil {
			r.logContainerEvent("re-attach", r.currentRunNumber(), r.mounts)
			r.setTask(task)
			if r.startupMode == containerStartupReattachStopped {
				r.stopAdoptedTask(task)
				r.cleanupDeploymentNetState()
				if r.shouldPublishStopped() {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				}
				return
			}
			if err := r.recoverContainerNet(); err != nil {
				slog.ErrorContext(r.ctx, "recovering adopted container network failed", "containerID", r.containerID, "err", err)
				r.stopAdoptedTask(task)
				r.cleanupDeploymentNetState()
				hadProcess = true
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			} else {
				if r.usesLatestNetworkConfig() {
					r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
				}
				r.monitorTask(task)
				r.deleteTask(task)
				r.setTask(nil)
				r.cleanupContainerNetState()
				hadProcess = true
				if !r.stopping.Load() {
					r.updateStatus(apigen.RunningStatus_CRASHED, int32(task.Pid()))
				}
			}
		} else {
			r.logContainerEvent("re-attach-miss", r.currentRunNumber(), r.mounts)
			r.cleanupDeploymentNetState()
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
		env, err := resolveEnv(r.runtimeInputs, r.envVars)
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
		if mkdirErr := os.MkdirAll(outputDir, 0o750); mkdirErr != nil {
			slog.ErrorContext(r.ctx, "creating run log dir failed", "err", mkdirErr, "path", outputDir)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		mounts := r.mounts
		var cn *network.ContainerNet
		var resolvConfPath string
		readinessActive := r.readiness != nil && !r.publish.Load()
		var ready <-chan error
		var closeReady func()
		if readinessActive {
			listener, listenerErr := r.startReadinessListener(runNumber)
			if listenerErr != nil {
				slog.ErrorContext(r.ctx, "starting readiness listener failed", "err", listenerErr)
				r.notifyReady(fmt.Errorf("starting readiness listener: %w", listenerErr))
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
				return
			}
			ready = listener.ready
			closeReady = listener.close
			mounts = append(append([]ctrd.Mount{}, mounts...), ctrd.Mount{Source: listener.dir, Dest: containerReadinessContainerDir})
			env = append(env, containerReadinessEnvKey+"="+containerReadinessContainerPath)
		}
		cn, resolvConfPath, err = r.setupContainerNet(runNumber)
		if err != nil {
			slog.ErrorContext(r.ctx, "setting up container network failed", "err", err, "containerID", r.containerID)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			if readinessActive {
				r.notifyReady(fmt.Errorf("setting up container network: %w", err))
				return
			}
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}

		spec := ctrd.ContainerSpec{
			ID:             r.containerID,
			Image:          r.status.RunningArtifact,
			User:           r.user,
			Env:            env,
			Args:           r.command,
			Cwd:            r.cwd,
			DevShmSizeKB:   r.devShmSizeKB,
			FileDescLimit:  r.fileDescLimit,
			Mounts:         mounts,
			Output:         outputDir,
			OutputVersion:  r.status.DeploymentConfigVersion,
			OutputRun:      runNumber,
			ResolvConfPath: resolvConfPath,
		}
		if cn != nil {
			spec.NetnsPath = cn.NetnsPath
		}
		task, err := ctrd.Default.RunTask(r.ctx, spec)
		if err != nil {
			if closeReady != nil {
				closeReady()
			}
			r.cleanupContainerNet(cn)
			slog.ErrorContext(r.ctx, "starting container failed", "err", err, "image", spec.Image, "containerID", r.containerID)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			if errors.Is(err, ctrd.ErrImageUnavailable) {
				r.notifyArtifactMissing()
				if readinessActive {
					r.notifyReady(fmt.Errorf("starting container: %w", err))
				}
				return
			}
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
		if cn != nil && !readinessActive {
			// Only the serving placement claims the address. A standby warming up
			// for a cross-node takeover runs a full runner here and must leave the
			// route alone until the primary promotes it.
			if err := r.claimInboundAddress(cn); err != nil {
				slog.ErrorContext(r.ctx, "publishing container network failed", "err", err, "containerID", r.containerID)
				_ = task.Kill(context.Background(), syscall.SIGTERM)
				r.deleteTask(task)
				r.cleanupContainerNet(cn)
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
				crashCount++
				if !r.sleepBackoff(crashCount) {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
					return
				}
				continue
			}
		}
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
				r.cleanupContainerNet(cn)
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
			r.cleanupContainerNet(cn)
			return
		}

		// Crash: stability reset, record CRASHED, clean up, back off, respawn.
		if time.Since(startedAt) >= containerStableRunWindow {
			crashCount = 0
		}
		crashCount++
		r.updateStatus(apigen.RunningStatus_CRASHED, int32(task.Pid()))
		r.deleteTask(task)
		r.cleanupContainerNet(cn)

		if !r.sleepBackoff(crashCount) {
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			return
		}
	}
}

func (r *containerRunner) notifyArtifactMissing() {
	select {
	case r.artifactMissing <- struct{}{}:
	default:
	}
}

// monitorTask blocks until the task exits or the runner context is cancelled.
func (r *containerRunner) monitorTask(task *ctrd.Task) {
	exitCh, err := task.Wait(r.ctx)
	if err != nil {
		slog.WarnContext(r.ctx, "waiting on container task failed", "containerID", r.containerID, "err", err)
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
		slog.WarnContext(r.ctx, "sending SIGTERM to adopted container failed", "containerID", r.containerID, "err", err)
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
			slog.WarnContext(r.ctx, "sending SIGKILL to adopted container failed", "containerID", r.containerID, "err", err)
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
	slog.InfoContext(r.ctx, fmt.Sprintf(
		"container %s deployment='%s' config_version=%d run_number=%d mount_paths='%s' dev_shm_size_kb=%d file_descriptor_limit=%d env_plain_count=%d env_config_count=%d env_secret_count=%d env_asset_count=%d",
		action,
		r.deploymentName,
		r.configVersion,
		runNumber,
		formatMountPaths(mounts),
		r.effectiveDevShmSizeKB(),
		r.effectiveFileDescLimit(),
		counts.plain,
		counts.config,
		counts.secret,
		counts.asset,
	))
}

func (r *containerRunner) currentRunNumber() int32 {
	return r.status.NumberOfRestarts + 1
}

func (r *containerRunner) effectiveDevShmSizeKB() int64 {
	if r.devShmSizeKB > 0 {
		return r.devShmSizeKB
	}
	return containerDefaultDevShmSizeKB
}

func (r *containerRunner) effectiveFileDescLimit() int64 {
	if r.fileDescLimit > 0 {
		return r.fileDescLimit
	}
	return ctrd.DefaultFileDescriptorLimit
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
	dir := filepath.Join(ainit.StaticConfig.ReadinessDir, strconv.Itoa(int(r.deploymentID)), strconv.Itoa(int(r.status.DeploymentConfigVersion)), strconv.Itoa(int(runNumber)))
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
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				select {
				case <-r.ctx.Done():
					ready <- r.ctx.Err()
				default:
					if !errors.Is(acceptErr, net.ErrClosed) {
						ready <- acceptErr
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
		slog.InfoContext(r.ctx, "container readiness signal received", "containerID", r.containerID)
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
		slog.WarnContext(r.ctx, "deleting container failed", "containerID", r.containerID, "err", err)
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

func (r *containerRunner) virtualNetwork() bool {
	return r.networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
}

func (r *containerRunner) inboundAddr() (netip.Addr, error) {
	prefix, ok := network.Default.PrefixValue()
	if !ok {
		return netip.Addr{}, fmt.Errorf("virtual network prefix is not known")
	}
	return prefix.InboundAddr(r.spaceID, r.deploymentID, 0)
}

func (r *containerRunner) setupContainerNet(runNumber int32) (*network.ContainerNet, string, error) {
	if !r.virtualNetwork() {
		return nil, "", nil
	}
	prefix, ok := network.Default.PrefixValue()
	if !ok {
		return nil, "", fmt.Errorf("virtual network prefix is not known")
	}
	inboundAddr, outboundAddr, err := containerNetAddresses(prefix, r.spaceID, r.deploymentID, r.scheduledInstanceID, runNumber)
	if err != nil {
		return nil, "", err
	}
	cn, err := network.Default.SetupContainerNet(network.ContainerNetSpec{
		ContainerID:              r.containerID,
		DeploymentID:             r.deploymentID,
		InboundAddr:              inboundAddr,
		OutboundAddr:             outboundAddr,
		UnprivilegedPortStart:    0,
		SetUnprivilegedPortStart: network.Default.IsNetproxyDeployment(r.deploymentID),
	})
	if err != nil {
		return nil, "", err
	}
	r.setContainerNet(cn)
	resolvConfPath, err := r.writeResolvConf(runNumber)
	if err != nil {
		r.cleanupContainerNet(cn)
		return nil, "", err
	}
	return cn, resolvConfPath, nil
}

// recoverContainerNet rebuilds the network state of a task that outlived the
// agent. Both addresses are derived rather than read back from status: the
// inbound address is a pure function of space, deployment, and ordinal, and the
// outbound one of the scheduled instance and run number, all of which this
// runner already holds. Nothing about a surviving task needs to be reported for
// its addresses to be reconstructed.
func (r *containerRunner) recoverContainerNet() error {
	if !r.virtualNetwork() {
		return nil
	}
	inboundAddr, err := r.inboundAddr()
	if err != nil {
		return err
	}
	prefix, ok := network.Default.PrefixValue()
	if !ok {
		return fmt.Errorf("virtual network prefix is not known")
	}
	identity, err := prefix.ParseAddr(inboundAddr)
	if err != nil {
		return fmt.Errorf("parsing derived inbound address %s: %w", inboundAddr, err)
	}
	if !identity.IsInbound() {
		return fmt.Errorf("derived address %s is not an inbound address", inboundAddr)
	}
	if identity.DeploymentID != r.deploymentID {
		return fmt.Errorf("derived inbound address %s belongs to deployment %d, not %d", inboundAddr, identity.DeploymentID, r.deploymentID)
	}
	outboundAddr, err := prefix.OutboundAddr(identity.SpaceID, r.deploymentID, identity.Ordinal, r.scheduledInstanceID, r.currentRunNumber())
	if err != nil {
		return err
	}
	cn, err := network.Default.RecoverContainerNet(r.containerID, r.deploymentID, inboundAddr, outboundAddr)
	if err != nil {
		return err
	}
	r.setContainerNet(cn)
	// Reclaiming the address is left to Serve: an adopted task only owns the
	// instance address if its placement is the serving one.
	if err := r.claimInboundAddress(cn); err != nil {
		return fmt.Errorf("publishing recovered container network: %w", err)
	}
	slog.InfoContext(r.ctx, "adopted container network recovered", "containerID", r.containerID, "inbound", cn.InboundAddr, "outbound", cn.OutboundAddr, "v4", cn.V4, "veth", cn.HostVeth)
	return nil
}

func (r *containerRunner) usesLatestNetworkConfig() bool {
	return r.configVersion == r.latestVersion || network.Default.IsNetproxyDeployment(r.deploymentID)
}

func containerNetAddresses(prefix network.Prefix, spaceID, deploymentID, scheduledInstanceID, runNumber int32) (netip.Addr, netip.Addr, error) {
	inboundAddr, err := prefix.InboundAddr(spaceID, deploymentID, 0)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	outboundAddr, err := prefix.OutboundAddr(spaceID, deploymentID, 0, scheduledInstanceID, runNumber)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	return inboundAddr, outboundAddr, nil
}

func (r *containerRunner) writeResolvConf(runNumber int32) (string, error) {
	if network.Default.IsNetproxyDeployment(r.deploymentID) {
		return "", nil
	}
	dns, ok := network.Default.DNSAddr()
	if !ok {
		return "", fmt.Errorf("netproxy DNS address is not known")
	}
	dir := filepath.Join(ainit.StaticConfig.ResolvConfDir, strconv.Itoa(int(r.deploymentID)), strconv.Itoa(int(r.configVersion)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, strconv.Itoa(int(runNumber))+".conf")
	content := "nameserver " + dns.String() + "\noptions ndots:1\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (r *containerRunner) publishContainerNet(cn *network.ContainerNet) error {
	if cn == nil {
		return nil
	}
	if network.Default.IsNetproxyDeployment(r.deploymentID) {
		return network.Default.PublishNetproxy(cn)
	}
	if err := network.Default.ApplyHostPorts(r.deploymentID, r.containerID, r.hostPortRules(cn)); err != nil {
		return err
	}
	network.Default.SetCurrentNet(r.deploymentID, cn)
	return nil
}

func (r *containerRunner) hostPortRules(cn *network.ContainerNet) []network.HostPortRule {
	if cn == nil || len(r.networking.PortForwarding) == 0 {
		return nil
	}
	rules := make([]network.HostPortRule, 0, len(r.networking.PortForwarding))
	for _, pf := range r.networking.PortForwarding {
		if pf == nil || pf.HostPort < 1 || pf.HostPort > 65535 || pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			continue
		}
		proto, ok := portForwardProtocol(pf.Protocol)
		if !ok {
			continue
		}
		rules = append(rules, network.HostPortRule{
			Protocol:   proto,
			HostPort:   uint16(pf.HostPort),
			TargetPort: uint16(pf.ContainerPort),
			TargetV6:   cn.InboundAddr,
			TargetV4:   cn.V4,
		})
	}
	return rules
}

func portForwardProtocol(protocol apigen.PortForwardProtocol) (uint8, bool) {
	switch protocol {
	case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP:
		return unix.IPPROTO_TCP, true
	case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		return unix.IPPROTO_UDP, true
	default:
		return 0, false
	}
}

func (r *containerRunner) cleanupContainerNet(cn *network.ContainerNet) {
	if cn == nil {
		return
	}
	if err := network.Default.ClearHostPorts(r.deploymentID, cn.ContainerID); err != nil {
		slog.WarnContext(r.ctx, "clearing host ports failed", "containerID", cn.ContainerID, "err", err)
	}
	network.Default.DropCurrentNet(r.deploymentID, cn.ContainerID)
	network.Default.TeardownContainerNet(cn)
	r.setContainerNet(nil)
}

func (r *containerRunner) cleanupContainerNetState() {
	if cn := r.getContainerNet(); cn != nil {
		r.cleanupContainerNet(cn)
		return
	}
	network.Default.TeardownContainerNetState(r.containerID, r.deploymentID)
}

func (r *containerRunner) cleanupDeploymentNetState() {
	if cn := r.getContainerNet(); cn != nil {
		r.cleanupContainerNet(cn)
		network.Default.CleanupContainerNets(r.deploymentID, nil)
		return
	}
	if err := network.Default.ClearHostPorts(r.deploymentID, r.containerID); err != nil {
		slog.WarnContext(r.ctx, "clearing host ports failed", "containerID", r.containerID, "err", err)
	}
	network.Default.DropCurrentNet(r.deploymentID, r.containerID)
	network.Default.CleanupContainerNets(r.deploymentID, nil)
	r.setContainerNet(nil)
}

func (r *containerRunner) setContainerNet(cn *network.ContainerNet) {
	r.netMu.Lock()
	r.net = cn
	r.netMu.Unlock()
}

func (r *containerRunner) getContainerNet() *network.ContainerNet {
	r.netMu.Lock()
	defer r.netMu.Unlock()
	return r.net
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
	r.store.MustWriteScheduledInstanceStatus(r.scheduledInstanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		if !s.Runner.IsZero() && s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion {
			slog.InfoContext(r.ctx, "discarding status update from superseded container runner")
			return false
		}
		s.BumpUpdatedAt()
		s.ScheduledInstanceID = r.scheduledInstanceID
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
	cfg := dep.Spec.Container().Runtime
	var mounts []ctrd.Mount
	var dataHost string
	if !cfg.DefaultVolume.Disabled {
		dataHost = defaultVolumeHostDir(dep.ID)
		mounts = append(mounts, ctrd.Mount{
			Source: dataHost,
			Dest:   defaultVolumeDest(cfg.DefaultVolume.ContainerPath),
		})
	}
	for _, m := range cfg.CrossDeploymentMounts {
		if m == nil {
			continue
		}
		mounts = append(mounts, ctrd.Mount{
			Source:   defaultVolumeHostDir(m.DeploymentID),
			Dest:     m.ContainerPath,
			ReadOnly: m.Permission != apigen.FilePermission_READ_WRITE,
		})
	}
	for _, m := range cfg.Mounts {
		if m == nil {
			continue
		}
		mounts = append(mounts, ctrd.Mount{
			Source:   m.HostPath,
			Dest:     m.ContainerPath,
			ReadOnly: m.Permission != apigen.FilePermission_READ_WRITE,
		})
	}
	for _, m := range cfg.AssetMounts {
		if m == nil {
			continue
		}
		hostPath := runtimeinputs.AssetCachePathWithMode(m.AssetID, m.Permission == apigen.FilePermission_READ_EXECUTE)
		mounts = append(mounts, ctrd.Mount{Source: hostPath, Dest: m.ContainerPath, ReadOnly: true})
	}
	implicitMounted := map[string]bool{}
	envKeys := make([]string, 0, len(cfg.EnvVars))
	for key := range cfg.EnvVars {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		value := cfg.EnvVars[key]
		if value == nil || value.AssetID <= 0 {
			continue
		}
		dest := implicitAssetContainerPath(value.AssetID)
		if implicitMounted[dest] {
			continue
		}
		implicitMounted[dest] = true
		mounts = append(mounts, ctrd.Mount{Source: runtimeinputs.AssetCachePath(value.AssetID), Dest: dest, ReadOnly: true})
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
// the explicit override if set, else /data.
func defaultVolumeDest(override string) string {
	if override != "" {
		return override
	}
	return "/data"
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
