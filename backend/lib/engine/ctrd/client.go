package ctrd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/contrib/seccomp"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/lib/engine/registryauth"
	"github.com/jptrs93/opsagent/backend/lib/log/logconsumer"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// Client is a lazily-connected handle to a containerd daemon, scoped to a
// single namespace.
type Client struct {
	mu             sync.Mutex
	c              *containerd.Client
	strictLogReady bool
}

// Default is the process-wide client used by preparers and container runners.
// It connects lazily on the first containerd operation.
var Default = &Client{}

func (c *Client) ensure() (*containerd.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c != nil {
		return c.c, nil
	}
	cl, err := containerd.New(ainit.StaticConfig.CtrdAddress)
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", ainit.StaticConfig.CtrdAddress, err)
	}
	c.c = cl
	c.strictLogReady = daemonSupportsStrictLogReady(cl)
	return cl, nil
}

// daemonSupportsStrictLogReady reports whether the daemon, and the shim that
// ships with it, accept the binary-v2 log scheme introduced in containerd 2.3.0.
func daemonSupportsStrictLogReady(cl *containerd.Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := cl.Version(ctx)
	if err != nil {
		return false
	}
	return versionAtLeast(v.Version, 2, 3)
}

func versionAtLeast(version string, major, minor int) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexAny(version, "-+"); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// logScheme is binary-v2 where the runtime supports it: the shim then requires
// the logger's ready byte, so a logger that fails at startup fails task
// creation instead of leaving the workload writing to a pipe with no reader.
// Older runtimes reject the scheme outright, so they keep the plain binary
// scheme, where EOF counts as ready.
func (c *Client) logScheme() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.strictLogReady {
		return "binary-v2"
	}
	return "binary"
}

func (c *Client) withNS(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, ainit.StaticConfig.CtrdNamespace)
}

// Pull fetches and unpacks an image into the content store/snapshotter and
// returns the image name as stored (the same ref used to create a container).
func (c *Client) Pull(ctx context.Context, ref string, credentials githubcredentials.Provider) (string, error) {
	cl, err := c.ensure()
	if err != nil {
		return "", err
	}
	ctx = c.withNS(ctx)
	client, err := registryauth.Client(ctx, ref, credentials)
	if err != nil {
		return "", err
	}
	resolver := docker.NewResolver(docker.ResolverOptions{Client: client})
	img, err := cl.Pull(ctx, ref, containerd.WithPullUnpack, containerd.WithResolver(resolver))
	if err != nil {
		return "", err
	}
	return img.Name(), nil
}

// Import ingests an OCI/Docker image tar stream into containerd, tags it as ref,
// and unpacks it into the default snapshotter for immediate execution.
func (c *Client) Import(ctx context.Context, image ImageStream) (string, error) {
	if image.Reader == nil {
		return "", fmt.Errorf("image stream reader is nil")
	}
	if image.Ref == "" {
		return "", fmt.Errorf("image ref is empty")
	}
	cl, err := c.ensure()
	if err != nil {
		return "", err
	}
	ctx = c.withNS(ctx)
	if _, importErr := cl.Import(ctx, image.Reader, containerd.WithIndexName(image.Ref)); importErr != nil {
		return "", importErr
	}
	img, err := cl.GetImage(ctx, image.Ref)
	if err != nil {
		return "", fmt.Errorf("loading imported image %s: %w", image.Ref, err)
	}
	if err := img.Unpack(ctx, ""); err != nil {
		return "", fmt.Errorf("unpacking imported image %s: %w", image.Ref, err)
	}
	return image.Ref, nil
}

func (c *Client) ImageSize(ctx context.Context, ref string) (int64, error) {
	cl, err := c.ensure()
	if err != nil {
		return 0, err
	}
	img, err := cl.GetImage(c.withNS(ctx), ref)
	if err != nil {
		return 0, fmt.Errorf("loading image %s: %w", ref, err)
	}
	size, err := img.Size(c.withNS(ctx))
	if err != nil {
		return 0, fmt.Errorf("getting image %s size: %w", ref, err)
	}
	return size, nil
}

// ImageReady verifies that an image and its unpacked snapshot are available in
// this node's local containerd store.
func (c *Client) ImageReady(ctx context.Context, ref string) error {
	cl, err := c.ensure()
	if err != nil {
		return err
	}
	ctx = c.withNS(ctx)
	img, err := cl.GetImage(ctx, ref)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return fmt.Errorf("%w: image %q not found", ErrImageUnavailable, ref)
		}
		return fmt.Errorf("loading image %q: %w", ref, err)
	}
	unpacked, err := img.IsUnpacked(ctx, "")
	if err != nil {
		return fmt.Errorf("checking image %q unpack status: %w", ref, err)
	}
	if !unpacked {
		return fmt.Errorf("%w: image %q is not unpacked", ErrImageUnavailable, ref)
	}
	return nil
}

type Task struct {
	client      *Client
	container   containerd.Container
	task        containerd.Task
	cgroupsPath string
}

// RunTask creates and starts a container from spec. It first removes any
// existing container with the same id (stale leftovers from a prior version or a
// crash before opendeploy restarted), so the deterministic id is always free.
func (c *Client) RunTask(ctx context.Context, spec ContainerSpec) (*Task, error) {
	cl, err := c.ensure()
	if err != nil {
		return nil, err
	}
	ctx = c.withNS(ctx)

	c.remove(ctx, cl, spec.ID)

	img, err := cl.GetImage(ctx, spec.Image)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("%w: image %q not found in containerd", ErrImageUnavailable, spec.Image)
		}
		return nil, fmt.Errorf("loading image %q from containerd: %w", spec.Image, err)
	}

	specOpts := buildSpecOpts(spec, img)

	container, err := cl.NewContainer(ctx, spec.ID,
		containerd.WithNewSnapshot(spec.ID+"-snapshot", img),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("%w: creating container from image %q: %v", ErrImageUnavailable, spec.Image, err)
		}
		return nil, fmt.Errorf("creating container: %w", err)
	}

	ioCreator, err := newLogConsumer(spec, c.logScheme())
	if err != nil {
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, err
	}
	task, err := container.NewTask(ctx, ioCreator)
	if err != nil {
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("creating task: %w", err)
	}
	if err := task.Start(ctx); err != nil {
		_, _ = task.Delete(ctx)
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("starting task: %w", err)
	}
	return &Task{client: c, container: container, task: task, cgroupsPath: cgroupsPathOf(ctx, container)}, nil
}

func buildSpecOpts(spec ContainerSpec, img containerd.Image) []oci.SpecOpts {
	var mounts []specs.Mount
	for _, m := range spec.Mounts {
		bindOpt := "rbind"
		if st, statErr := os.Stat(m.Source); statErr == nil && !st.IsDir() {
			bindOpt = "bind"
		}
		opts := []string{bindOpt}
		if m.ReadOnly {
			opts = append(opts, "ro")
		} else {
			opts = append(opts, "rw")
		}
		mounts = append(mounts, specs.Mount{
			Destination: m.Dest,
			Type:        "bind",
			Source:      m.Source,
			Options:     opts,
		})
	}

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(img),
		oci.WithHostHostsFile,
		oci.WithEnv(spec.Env),
	}
	switch {
	case spec.NetnsPath != "":
		// Virtual network: join the pre-created netns; resolv.conf points at the
		// machine's netproxy DNS server.
		specOpts = append(specOpts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: spec.NetnsPath,
		}))
		if spec.ResolvConfPath != "" {
			mounts = append(mounts, specs.Mount{
				Destination: "/etc/resolv.conf",
				Type:        "bind",
				Source:      spec.ResolvConfPath,
				Options:     []string{"bind", "ro"},
			})
		} else {
			specOpts = append(specOpts, oci.WithHostResolvconf)
		}
	case spec.NoNetwork:
	default:
		// Host networking: explicit networking.mode=host opt-out.
		specOpts = append(specOpts, oci.WithHostNamespace(specs.NetworkNamespace), oci.WithHostResolvconf)
	}
	if spec.User != "" {
		specOpts = append(specOpts, oci.WithUser(spec.User))
	}
	if spec.Cwd != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(spec.Cwd))
	}
	if len(spec.Args) > 0 {
		specOpts = append(specOpts, oci.WithProcessArgs(spec.Args...))
	}
	if spec.DevShmSizeKB > 0 {
		specOpts = append(specOpts, oci.WithDevShmSize(spec.DevShmSizeKB))
	}
	fileDescLimit := spec.FileDescLimit
	if fileDescLimit <= 0 {
		fileDescLimit = DefaultFileDescriptorLimit
	}
	specOpts = append(specOpts, oci.WithRlimit(&specs.POSIXRlimit{
		Type: "RLIMIT_NOFILE",
		Soft: uint64(fileDescLimit),
		Hard: uint64(fileDescLimit),
	}))
	if r := spec.Resources; r != nil {
		if r.MemoryBytes > 0 {
			specOpts = append(specOpts, oci.WithMemoryLimit(uint64(r.MemoryBytes)), oci.WithMemorySwap(r.MemoryBytes))
		}
		if r.CPUs > 0 {
			const period = 100000
			specOpts = append(specOpts, oci.WithCPUCFS(int64(r.CPUs)*period, period))
		}
		if r.Pids > 0 {
			specOpts = append(specOpts, oci.WithPidsLimit(r.Pids))
		}
	}
	if spec.DefaultSeccomp {
		specOpts = append(specOpts, seccomp.WithDefaultProfile())
	}
	if len(mounts) > 0 {
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}
	return specOpts
}

// RunBuild runs a one-shot container to completion: its stdout and stderr
// stream to the given writers, cancellation kills it, and the container and
// snapshot are deleted before returning. The exit status is returned even
// when non-zero; an error means the container could not be run.
func (c *Client) RunBuild(ctx context.Context, spec ContainerSpec, stdout, stderr io.Writer) (BuildResult, error) {
	cl, err := c.ensure()
	if err != nil {
		return BuildResult{}, err
	}
	ctx = c.withNS(ctx)
	c.remove(ctx, cl, spec.ID)

	img, err := cl.GetImage(ctx, spec.Image)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return BuildResult{}, fmt.Errorf("%w: image %q not found in containerd", ErrImageUnavailable, spec.Image)
		}
		return BuildResult{}, fmt.Errorf("loading image %q from containerd: %w", spec.Image, err)
	}
	container, err := cl.NewContainer(ctx, spec.ID,
		containerd.WithNewSnapshot(spec.ID+"-snapshot", img),
		containerd.WithNewSpec(buildSpecOpts(spec, img)...),
	)
	if err != nil {
		return BuildResult{}, fmt.Errorf("creating container: %w", err)
	}
	cleanupCtx := func() (context.Context, context.CancelFunc) {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		return c.withNS(bg), cancel
	}
	defer func() {
		bg, cancel := cleanupCtx()
		defer cancel()
		_ = container.Delete(bg, containerd.WithSnapshotCleanup)
	}()

	fifoDir := filepath.Join(ainit.StaticConfig.DataDir, "fifo")
	if err := os.MkdirAll(fifoDir, 0o755); err != nil {
		return BuildResult{}, fmt.Errorf("creating fifo dir: %w", err)
	}
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdout, stderr), cio.WithFIFODir(fifoDir)))
	if err != nil {
		return BuildResult{}, fmt.Errorf("creating task: %w", err)
	}
	defer func() {
		bg, cancel := cleanupCtx()
		defer cancel()
		_, _ = task.Delete(bg, containerd.WithProcessKill)
	}()
	exitC, err := task.Wait(ctx)
	if err != nil {
		return BuildResult{}, fmt.Errorf("waiting on task: %w", err)
	}
	if err := task.Start(ctx); err != nil {
		return BuildResult{}, fmt.Errorf("starting task: %w", err)
	}
	var status containerd.ExitStatus
	select {
	case status = <-exitC:
	case <-ctx.Done():
		bg, cancel := cleanupCtx()
		defer cancel()
		_ = task.Kill(bg, syscall.SIGKILL, containerd.WithKillAll)
		if killed, err := task.Wait(bg); err == nil {
			select {
			case <-killed:
			case <-bg.Done():
			}
		}
		return BuildResult{}, ctx.Err()
	}
	code, _, err := status.Result()
	if err != nil {
		return BuildResult{}, fmt.Errorf("reading task exit status: %w", err)
	}
	return BuildResult{Code: code, OOMKilled: oomKilled(cgroupsPathOf(ctx, container))}, nil
}

// oomKilled reads the container cgroup's memory.events before the task is
// deleted, which removes the cgroup.
func oomKilled(cgroupsPath string) bool {
	if cgroupsPath == "" {
		return false
	}
	f, err := os.Open(filepath.Join("/sys/fs/cgroup", cgroupsPath, "memory.events"))
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "oom_kill" {
			n, _ := strconv.Atoi(fields[1])
			return n > 0
		}
	}
	return false
}

// cgroupsPathOf returns the cgroup containerd assigned in the OCI spec, by
// default /<namespace>/<container id>.
func cgroupsPathOf(ctx context.Context, container containerd.Container) string {
	spec, err := container.Spec(ctx)
	if err != nil || spec.Linux == nil {
		return ""
	}
	return spec.Linux.CgroupsPath
}

// ListContainerIDs returns the ids of every container in the namespace whose
// id starts with prefix.
func (c *Client) ListContainerIDs(ctx context.Context, prefix string) ([]string, error) {
	cl, err := c.ensure()
	if err != nil {
		return nil, err
	}
	containers, err := cl.Containers(c.withNS(ctx))
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	var ids []string
	for _, container := range containers {
		if id := container.ID(); strings.HasPrefix(id, prefix) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Remove kills and deletes the container with the given id, if it exists.
func (c *Client) Remove(ctx context.Context, id string) error {
	cl, err := c.ensure()
	if err != nil {
		return err
	}
	c.remove(c.withNS(ctx), cl, id)
	return nil
}

func newLogConsumer(spec ContainerSpec, scheme string) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	config, err := logconsumer.RawBinaryConfigArg(logconsumer.RawBinaryConfig{
		DeploymentDir:   spec.LogDir,
		Version:         spec.LogVersion,
		Run:             spec.LogRun,
		Deployment:      spec.LogDeployment,
		Node:            spec.LogNode,
		InstanceOrdinal: spec.LogOrdinal,
	})
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator(scheme, binary, map[string]string{string(ainit.CommandRawLogConsumer): config})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}

// LoadTask reconnects to a running container/task by id for reattach after an
// opendeploy restart. Returns ErrNotFound when the container or its task is gone
// (or not running), in which case the caller spawns fresh.
func (c *Client) LoadTask(ctx context.Context, id string) (*Task, error) {
	cl, err := c.ensure()
	if err != nil {
		return nil, err
	}
	ctx = c.withNS(ctx)
	container, err := cl.LoadContainer(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, ErrNotFound
	}
	st, err := task.Status(ctx)
	if err != nil || st.Status != containerd.Running {
		return nil, ErrNotFound
	}
	return &Task{client: c, container: container, task: task, cgroupsPath: cgroupsPathOf(ctx, container)}, nil
}

func (t *Task) CgroupsPath() string {
	return t.cgroupsPath
}

func (t *Task) Pid() uint32 {
	if t.task == nil {
		return 0
	}
	return t.task.Pid()
}

func (t *Task) Wait(ctx context.Context) (<-chan ExitStatus, error) {
	statusC, err := t.task.Wait(t.client.withNS(ctx))
	if err != nil {
		return nil, err
	}
	out := make(chan ExitStatus, 1)
	go func() {
		s := <-statusC
		code, _, rerr := s.Result()
		out <- ExitStatus{Code: code, Err: rerr}
	}()
	return out, nil
}

type TaskStatus struct {
	Stopped  bool
	ExitCode uint32
}

func (t *Task) Status(ctx context.Context) (TaskStatus, error) {
	st, err := t.task.Status(t.client.withNS(ctx))
	if err != nil {
		if errdefs.IsNotFound(err) {
			return TaskStatus{}, ErrNotFound
		}
		return TaskStatus{}, err
	}
	if st.Status == containerd.Stopped {
		return TaskStatus{Stopped: true, ExitCode: st.ExitStatus}, nil
	}
	return TaskStatus{}, nil
}

func (t *Task) Kill(ctx context.Context, sig syscall.Signal) error {
	return t.task.Kill(t.client.withNS(ctx), sig)
}

// Delete tears down the task and container (and its snapshot). Best effort.
func (t *Task) Delete(ctx context.Context) error {
	ctx = t.client.withNS(ctx)
	if t.task != nil {
		_, _ = t.task.Delete(ctx)
	}
	if t.container != nil {
		return t.container.Delete(ctx, containerd.WithSnapshotCleanup)
	}
	return nil
}

// remove kills + deletes any existing container with the given id so a fresh
// create succeeds. Best effort: missing containers are ignored.
func (c *Client) remove(ctx context.Context, cl *containerd.Client, id string) {
	container, err := cl.LoadContainer(ctx, id)
	if err != nil {
		return
	}
	if task, err := container.Task(ctx, nil); err == nil {
		_ = task.Kill(ctx, syscall.SIGKILL)
		if sc, err := task.Wait(ctx); err == nil {
			select {
			case <-sc:
			case <-time.After(5 * time.Second):
			}
		}
		_, _ = task.Delete(ctx)
	}
	_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
}
