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

	"github.com/jptrs93/goutil/logu"
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
	issuedTLSMount *apigen.IssuedTLSMount
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

	// readinessPending marks a rollover candidate that has not reported ready
	// yet. It gates promotion, never status publication: a candidate publishes to
	// its own scheduled instance row like any other runner. An instance's config
	// is pinned to its version, so a candidate is only ever created when nothing
	// else is running for that instance and there is no incumbent status to
	// clobber.
	readinessPending atomic.Bool
	// readinessDeadline bounds the wait for the readiness signal across every
	// restart, so a candidate that crash-loops on startup eventually gives up
	// rather than warming up forever behind the placement it should replace.
	readinessDeadline time.Time

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
	r.initFreshRun(dep, preparerStatus, false)
	go r.run()
	return r
}

func newRolloverContainerRunner(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus) *containerRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	configVersion := preparerStatus.DeploymentConfigVersion
	r := buildContainerRunner(ctx, cancel, store, inputs, instanceID, dep, configVersion)
	r.initFreshRun(dep, preparerStatus, true)
	go r.run()
	return r
}

// initFreshRun prepares a runner that is about to spawn its first container.
// candidate marks a rollover candidate, which warms up behind whatever is
// serving and must not claim the instance address until it reports ready.
//
// A candidate publishes status from here on exactly like an ordinary runner.
// Suppressing its writes is what used to make a candidate that crashed during
// startup invisible: nothing was recorded, so nothing was notified, so the
// operator never woke to build a replacement and the rollout stalled in silence.
func (r *containerRunner) initFreshRun(dep *apigen.DeploymentConfig, preparerStatus apigen.PreparerStatus, candidate bool) {
	if candidate {
		timeout := containerReadinessTimeout(dep.Spec.Container().ReadinessSignal)
		r.readiness = &readinessConfig{timeout: timeout}
		r.readinessDeadline = time.Now().Add(timeout)
		r.readinessPending.Store(true)
		r.readyCh = make(chan error, 1)
	}
	r.status = apigen.RunnerStatus{
		DeploymentConfigVersion: preparerStatus.DeploymentConfigVersion,
		RunningArtifact:         preparerStatus.Artifact,
		Status:                  apigen.RunningStatus_STARTING,
		LastRestartAt:           time.Now(),
	}
	r.writeStatus()
}

func reAttachContainerRunner(store storage.OperatorStore, inputs *runtimeinputs.RuntimeInputs, instanceID int32, dep *apigen.DeploymentConfig, prev apigen.RunnerStatus, mode containerStartupMode) *containerRunner {
	ctx, cancel := context.WithCancel(deploymentLogContext(instanceID, dep))
	r := buildContainerRunner(ctx, cancel, store, inputs, instanceID, dep, prev.DeploymentConfigVersion)
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
	// Layering the container id onto the cancellation context keeps it on every
	// log line without repeating it per call; cancel() still reaches the child.
	ctx = logu.AddKV(ctx, "container", containerID(dep.ID, configVersion))
	r := &containerRunner{
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		artifactMissing:     make(chan struct{}, 1),
		store:               store,
		runtimeInputs:       inputs,
		scheduledInstanceID: instanceID,
		deploymentID:        dep.ID,
		spaceID:             dep.SpaceID,
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
	r.issuedTLSMount = cfg.IssuedTlsMount
	r.devShmSizeKB = int64(cfg.DevShmSizeKb)
	r.fileDescLimit = int64(cfg.FileDescriptorLimit)
	r.dataVolumeUser = cfg.User
	return r
}

func containerDeploymentName(dep *apigen.DeploymentConfig) string {
	if dep == nil {
		return "<nil>"
	}
	if dep.Name != "" {
		return fmt.Sprintf("%d:%d:%s", dep.SpaceID, dep.NodeID, dep.Name)
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
// construction the replacement on this node, so promotion claims the instance
// address. The run loop has already cleared the readiness gate and published
// RUNNING by this point — that write is what told the scheduler to promote.
func (r *containerRunner) Promote() error {
	r.servingMu.Lock()
	defer r.servingMu.Unlock()
	r.serving = true
	return r.claimInboundAddressLocked(r.getContainerNet())
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
			slog.WarnContext(r.ctx, "sending SIGTERM to container failed", "err", err)
		}
		select {
		case <-r.done:
			r.deleteTask(task)
			return
		case <-time.After(3 * time.Second):
		}
		if err := task.Kill(context.Background(), syscall.SIGKILL); err != nil {
			slog.WarnContext(r.ctx, "sending SIGKILL to container failed", "err", err)
		}
	}
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
				slog.ErrorContext(r.ctx, "recovering adopted container network failed", "err", err)
				r.stopAdoptedTask(task)
				r.cleanupDeploymentNetState()
				hadProcess = true
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			} else {
				if r.usesLatestNetworkConfig() {
					r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
				}
				r.status.ExitCode = r.monitorTask(task)
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
		// Every respawn path loops back through here, so this is what bounds a
		// candidate that never gets far enough to wait for a signal at all — one
		// whose container fails to start, not just one that starts unhealthy.
		if r.readinessPending.Load() && !time.Now().Before(r.readinessDeadline) {
			r.failReadiness(fmt.Errorf("timed out after %s waiting for readiness signal", r.readiness.timeout))
			return
		}

		if hadProcess {
			r.status.NumberOfRestarts++
		}
		hadProcess = true
		r.status.LastRestartAt = time.Now()
		r.status.ExitCode = nil

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
		if err := r.ensureIssuedTLS(); err != nil {
			slog.ErrorContext(r.ctx, "materializing issued TLS failed", "err", err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			crashCount++
			if !r.sleepBackoff(crashCount) {
				r.updateStatus(apigen.RunningStatus_STOPPED, 0)
				return
			}
			continue
		}
		runNumber := r.status.NumberOfRestarts + 1
		logDir := apigen.LogWALDeploymentDir(r.deploymentID)
		if mkdirErr := os.MkdirAll(logDir, 0o750); mkdirErr != nil {
			slog.ErrorContext(r.ctx, fmt.Sprintf("creating log wal dir %s failed", logDir), "err", mkdirErr)
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
		readinessActive := r.readiness != nil && r.readinessPending.Load()
		var ready <-chan error
		var closeReady func()
		if readinessActive {
			listener, listenerErr := r.startReadinessListener(runNumber)
			if listenerErr != nil {
				slog.ErrorContext(r.ctx, "starting readiness listener failed", "err", listenerErr)
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
				crashCount++
				if !r.sleepBackoff(crashCount) {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
					return
				}
				continue
			}
			ready = listener.ready
			closeReady = listener.close
			mounts = append(append([]ctrd.Mount{}, mounts...), ctrd.Mount{Source: listener.dir, Dest: containerReadinessContainerDir})
			env = append(env, containerReadinessEnvKey+"="+containerReadinessContainerPath)
		}
		cn, resolvConfPath, err = r.setupContainerNet(runNumber)
		if err != nil {
			slog.ErrorContext(r.ctx, "setting up container network failed", "err", err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
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
			LogDir:         logDir,
			LogVersion:     r.status.DeploymentConfigVersion,
			LogRun:         runNumber,
			ResolvConfPath: resolvConfPath,

			LogDeployment: r.deploymentID,
			LogNode:       r.nodeID,
			// The scheduler only ever assigns defaultInstanceOrdinal today; this
			// becomes a real per-instance value when multi-instance lands.
			LogOrdinal: 0,
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
			slog.ErrorContext(r.ctx, fmt.Sprintf("starting container failed image=%q", spec.Image), "err", err)
			r.updateStatus(apigen.RunningStatus_CRASHED, 0)
			if errors.Is(err, ctrd.ErrImageUnavailable) {
				r.notifyArtifactMissing()
				if readinessActive {
					// The operator repairs the artifact and builds a fresh
					// candidate, so this one has nothing left to wait for.
					r.failReadiness(fmt.Errorf("starting container: %w", err))
				}
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
				slog.ErrorContext(r.ctx, "publishing container network failed", "err", err)
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
		// RUNNING is the scheduler's promotion trigger, so a candidate must not
		// publish it before the container has said it is ready — that would hand
		// over the instance address with the readiness gate still closed.
		if readinessActive {
			r.updateStatus(apigen.RunningStatus_STARTING, int32(task.Pid()))
		} else {
			r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
		}
		startedAt := time.Now()

		var exitCode *int32
		exitDone := make(chan struct{})
		go func() {
			exitCode = r.monitorTask(task)
			close(exitDone)
		}()

		if readinessActive {
			outcome, taskExited, readyErr := r.waitForReadiness(ready, closeReady, exitDone)
			if outcome == readinessSignalled {
				r.readinessPending.Store(false)
				r.notifyReady(nil)
				r.updateStatus(apigen.RunningStatus_RUNNING, int32(task.Pid()))
			} else {
				slog.WarnContext(r.ctx, "rollover candidate did not report ready", "err", readyErr)
				if !taskExited {
					_ = task.Kill(context.Background(), syscall.SIGTERM)
				}
				r.deleteTask(task)
				r.setTask(nil)
				r.cleanupContainerNet(cn)
				if outcome == readinessGiveUp {
					r.failReadiness(readyErr)
					return
				}
				// The deadline has not passed, so this is an ordinary crash: record
				// it, back off, and try again. The operator is told nothing yet, so
				// it keeps waiting on this candidate rather than building a second.
				if r.stopping.Load() {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
					return
				}
				crashCount++
				r.updateStatus(apigen.RunningStatus_CRASHED, 0)
				if !r.sleepBackoff(crashCount) {
					r.updateStatus(apigen.RunningStatus_STOPPED, 0)
					return
				}
				continue
			}
		}

		<-exitDone
		r.status.ExitCode = exitCode
		if closeReady != nil {
			closeReady()
		}

		if r.stopping.Load() {
			// Stop() owns the kill + delete; just record STOPPED and exit.
			r.updateStatus(apigen.RunningStatus_STOPPED, 0)
			r.cleanupContainerNet(cn)
			return
		}

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

func (r *containerRunner) monitorTask(task *ctrd.Task) *int32 {
	exitCh, err := task.Wait(r.ctx)
	if err != nil {
		slog.WarnContext(r.ctx, "waiting on container task failed", "err", err)
		return nil
	}
	select {
	case es := <-exitCh:
		if es.Err != nil {
			slog.WarnContext(r.ctx, "reading container exit status failed", "err", es.Err)
			return nil
		}
		code := int32(es.Code)
		return &code
	case <-r.ctx.Done():
		return nil
	}
}

func (r *containerRunner) stopAdoptedTask(task *ctrd.Task) {
	if task == nil {
		return
	}
	r.logContainerEvent("stop", r.currentRunNumber(), r.mounts)
	if err := task.Kill(context.Background(), syscall.SIGTERM); err != nil {
		slog.WarnContext(r.ctx, "sending SIGTERM to adopted container failed", "err", err)
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
			slog.WarnContext(r.ctx, "sending SIGKILL to adopted container failed", "err", err)
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
		if value.ConfigVersionID != nil {
			counts.config++
		}
		if value.SecretVersionID != nil {
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
	slog.InfoContext(r.ctx, fmt.Sprintf("backoff sleep %s before container respawn after %d crashes", delay, crashCount))
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

// readinessOutcome is what one startup attempt of a rollover candidate settled.
type readinessOutcome int

const (
	// readinessSignalled: the container reported ready and can be promoted.
	readinessSignalled readinessOutcome = iota
	// readinessRetry: this attempt is over but the deadline has not passed, so
	// the candidate respawns like any other crashed container.
	readinessRetry
	// readinessGiveUp: the candidate will never report ready.
	readinessGiveUp
)

// waitForReadiness blocks until this startup attempt is settled. It reports the
// outcome rather than acting on it: whether a failed attempt is worth retrying
// depends on the deadline, which spans every attempt, not just this one.
func (r *containerRunner) waitForReadiness(ready <-chan error, closeReady func(), exitDone <-chan struct{}) (readinessOutcome, bool, error) {
	defer closeReady()
	timer := time.NewTimer(time.Until(r.readinessDeadline))
	defer timer.Stop()
	select {
	case err, ok := <-ready:
		if !ok {
			return readinessRetry, false, fmt.Errorf("readiness listener closed before signal")
		}
		if err != nil {
			return readinessRetry, false, fmt.Errorf("readiness signal failed: %w", err)
		}
		slog.InfoContext(r.ctx, "container readiness signal received")
		return readinessSignalled, false, nil
	case <-exitDone:
		return readinessRetry, true, fmt.Errorf("container exited before readiness signal")
	case <-timer.C:
		return readinessGiveUp, false, fmt.Errorf("timed out after %s waiting for readiness signal", r.readiness.timeout)
	case <-r.ctx.Done():
		return readinessGiveUp, false, r.ctx.Err()
	}
}

// failReadiness abandons a rollover candidate. It releases the operator, which
// is waiting on WaitReady, and records the failure so the scheduled instance
// shows a failed rollover rather than sitting at whatever it last published.
func (r *containerRunner) failReadiness(err error) {
	r.readinessPending.Store(false)
	r.notifyReady(err)
	want := apigen.RunningStatus_CRASHED
	if r.stopping.Load() {
		want = apigen.RunningStatus_STOPPED
	}
	if r.status.Status == want && r.status.RunningPid == 0 {
		return
	}
	r.updateStatus(want, 0)
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
		slog.WarnContext(r.ctx, fmt.Sprintf("creating data volume dir %s failed", r.dataVolumeHost), "err", err)
		return
	}
	if err := chownToUser(r.dataVolumeHost, r.dataVolumeUser); err != nil {
		slog.WarnContext(r.ctx, fmt.Sprintf("chowning data volume dir %s to user %s failed (non-root container users may be unable to write)", r.dataVolumeHost, r.dataVolumeUser), "err", err)
	}
}

func (r *containerRunner) ensureIssuedTLS() error {
	if r.issuedTLSMount == nil {
		return nil
	}
	value, ok := r.runtimeInputs.ResolveIssuedTLS(r.deploymentID)
	if !ok {
		return fmt.Errorf("issued TLS for deployment %d is not resolvable on this node", r.deploymentID)
	}
	dir := runtimeinputs.IssuedTLSHostDir(r.deploymentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating issued TLS dir %s: %w", dir, err)
	}
	type tlsFile struct {
		name string
		data []byte
		mode os.FileMode
	}
	files := []tlsFile{{"ca.crt", value.CACertPEM, 0o644}}
	if r.issuedTLSMount.CaOnly {
		// Downgrading from a full bundle must remove the leaf material from
		// the mount, not just stop refreshing it.
		for _, stale := range []string{"public.crt", "private.key"} {
			if err := os.Remove(filepath.Join(dir, stale)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing stale issued TLS file %s: %w", stale, err)
			}
		}
	} else {
		files = append(files,
			tlsFile{"public.crt", value.CertPEM, 0o644},
			tlsFile{"private.key", value.KeyPEM, 0o600},
		)
	}
	for _, f := range files {
		// Write to a temp file and rename over the old one: after the first
		// materialization the files are owned by the container user, and the
		// agent (no CAP_FOWNER) cannot chmod files it does not own.
		path := filepath.Join(dir, f.name)
		tmp := path + ".tmp"
		_ = os.Remove(tmp)
		if err := os.WriteFile(tmp, f.data, f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", tmp, err)
		}
		if err := os.Chmod(tmp, f.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", tmp, err)
		}
		if err := chownToUser(tmp, r.user); err != nil {
			slog.WarnContext(r.ctx, fmt.Sprintf("chowning issued TLS file %s to user %s failed (non-root container users may be unable to read)", tmp, r.user), "err", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("replacing %s: %w", path, err)
		}
	}
	if err := chownToUser(dir, r.user); err != nil {
		slog.WarnContext(r.ctx, fmt.Sprintf("chowning issued TLS dir %s to user %s failed (non-root container users may be unable to read)", dir, r.user), "err", err)
	}
	return nil
}

func (r *containerRunner) deleteTask(task *ctrd.Task) {
	if task == nil {
		return
	}
	if err := task.Delete(context.Background()); err != nil {
		slog.WarnContext(r.ctx, "deleting container failed", "err", err)
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
	slog.InfoContext(r.ctx, fmt.Sprintf("adopted container network recovered inbound=%s outbound=%s v4=%s veth=%s", cn.InboundAddr, cn.OutboundAddr, cn.V4, cn.HostVeth))
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
		rule := network.HostPortRule{
			Protocol:   proto,
			HostPort:   uint16(pf.HostPort),
			TargetPort: uint16(pf.ContainerPort),
			TargetV6:   cn.InboundAddr,
			TargetV4:   cn.V4,
		}
		if pf.IpFilter != nil && len(pf.IpFilter.Allow) > 0 {
			rule.Filtered = true
			rule.AllowV4, rule.AllowV6 = network.SplitFilterPrefixes(pf.IpFilter.Allow)
		}
		rules = append(rules, rule)
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
		slog.WarnContext(r.ctx, "clearing host ports failed", "err", err)
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
		slog.WarnContext(r.ctx, "clearing host ports failed", "err", err)
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

func (r *containerRunner) updateStatus(status apigen.RunningStatus, pid int32) {
	r.status.Status = status
	r.status.RunningPid = pid
	r.writeStatus()
}

func (r *containerRunner) writeStatus() {
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
		hostPath := runtimeinputs.AssetCachePathWithMode(m.AssetVersionID, m.Permission == apigen.FilePermission_READ_EXECUTE)
		mounts = append(mounts, ctrd.Mount{Source: hostPath, Dest: m.ContainerPath, ReadOnly: true})
	}
	if cfg.IssuedTlsMount != nil {
		mounts = append(mounts, ctrd.Mount{
			Source:   runtimeinputs.IssuedTLSHostDir(dep.ID),
			Dest:     cfg.IssuedTlsMount.ContainerPath,
			ReadOnly: true,
		})
	}
	implicitMounted := map[string]bool{}
	envKeys := make([]string, 0, len(cfg.EnvVars))
	for key := range cfg.EnvVars {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		value := cfg.EnvVars[key]
		if value == nil || value.AssetVersionID <= 0 {
			continue
		}
		dest := implicitAssetContainerPath(value.AssetVersionID)
		if implicitMounted[dest] {
			continue
		}
		implicitMounted[dest] = true
		mounts = append(mounts, ctrd.Mount{Source: runtimeinputs.AssetCachePath(value.AssetVersionID), Dest: dest, ReadOnly: true})
	}
	return mounts, dataHost
}

// defaultVolumeHostDir is the opendeploy-owned host directory bind-mounted as the
// container's default data volume. A sibling of the data dir (world-traversable,
// 0755), like release artifacts, so the in-container user can reach it.
func defaultVolumeHostDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.VolumesDir, strconv.Itoa(int(deploymentID)), "default")
}

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
