//go:build linux

package ctrd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/jptrs93/opsagent/backend/engine/logconsumer"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Client is a lazily-connected handle to a containerd daemon, scoped to a
// single namespace.
type Client struct {
	address   string
	namespace string

	mu sync.Mutex
	c  *containerd.Client
}

// Connect stores the connection parameters but does not dial — the first
// operation establishes the connection. This keeps opendeploy startup independent
// of whether containerd is installed.
func Connect(address, namespace string) *Client {
	return &Client{address: address, namespace: namespace}
}

func (c *Client) Supported() bool { return true }

func (c *Client) ensure() (*containerd.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c != nil {
		return c.c, nil
	}
	cl, err := containerd.New(c.address)
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", c.address, err)
	}
	c.c = cl
	return cl, nil
}

func (c *Client) withNS(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, c.namespace)
}

// Pull fetches and unpacks an image into the content store/snapshotter and
// returns the image name as stored (the same ref used to create a container).
func (c *Client) Pull(ctx context.Context, ref string) (string, error) {
	cl, err := c.ensure()
	if err != nil {
		return "", err
	}
	ctx = c.withNS(ctx)
	img, err := cl.Pull(ctx, ref, containerd.WithPullUnpack)
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
	if _, err := cl.Import(ctx, image.Reader, containerd.WithIndexName(image.Ref)); err != nil {
		return "", err
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

// Task is a handle to a created (and started) container task.
type Task struct {
	client    *Client
	container containerd.Container
	task      containerd.Task
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
		return nil, fmt.Errorf("image %s not found in containerd (was it pulled?): %w", spec.Image, err)
	}

	var mounts []specs.Mount
	for _, m := range spec.Mounts {
		bindOpt := "rbind"
		if st, err := os.Stat(m.Source); err == nil && !st.IsDir() {
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
		oci.WithHostNamespace(specs.NetworkNamespace), // host networking
		oci.WithHostHostsFile,
		oci.WithHostResolvconf,
		oci.WithEnv(spec.Env),
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
	if len(mounts) > 0 {
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}

	container, err := cl.NewContainer(ctx, spec.ID,
		containerd.WithNewSnapshot(spec.ID+"-snapshot", img),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		return nil, fmt.Errorf("creating container: %w", err)
	}

	ioCreator, err := newLogConsumer(spec)
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
	return &Task{client: c, container: container, task: task}, nil
}

func newLogConsumer(spec ContainerSpec) (cio.Creator, error) {
	return logconsumer.NewRawBinaryV2(spec.Output, spec.OutputVersion, spec.OutputRun)
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
	return &Task{client: c, container: container, task: task}, nil
}

func (t *Task) Pid() uint32 {
	if t.task == nil {
		return 0
	}
	return t.task.Pid()
}

// Wait returns a channel that delivers the task's exit status once it exits.
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
