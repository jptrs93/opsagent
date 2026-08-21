// Package ctrd isolates all containerd usage behind a small surface.
//
// Default is the process-wide handle used by image preparers and container
// runners. It dials containerd lazily, so opendeploy still starts on hosts where
// containerd is absent — only container deployments fail, with a clear error.
package ctrd

import (
	"errors"
	"io"
)

// ErrNotFound is returned by LoadTask when no running container/task exists for
// the given id (the caller should perform a fresh spawn).
var ErrNotFound = errors.New("container task not found")

// ErrImageUnavailable marks a missing or incomplete local image so the
// operator can prepare the same desired config version again.
var ErrImageUnavailable = errors.New("container image unavailable")

// Mount is a single host bind mount into the container.
type Mount struct {
	Source   string
	Dest     string
	ReadOnly bool
}

const DefaultFileDescriptorLimit = 2048

// ContainerSpec describes a container to create and start. The default data
// volume (if any) is already resolved into Mounts by the caller.
type ContainerSpec struct {
	ID            string   // deterministic container id (one per deployment config version)
	Image         string   // resolved image ref to run (as stored by Pull)
	User          string   // OCI process.user (uid, uid:gid, or name); empty = image default
	Env           []string // KEY=VALUE entries
	Args          []string // argv override (entrypoint+cmd); empty = image default
	Cwd           string   // process cwd; empty = image default
	DevShmSizeKB  int64    // optional /dev/shm tmpfs size override in KiB
	FileDescLimit int64    // optional RLIMIT_NOFILE override; 0 uses DefaultFileDescriptorLimit
	Mounts        []Mount  // host bind mounts
	Output        string   // stdout/stderr deployment log directory
	OutputVersion int32    // deployment config version for stdout/stderr records
	OutputRun     int32    // deployment run number for stdout/stderr records
	// Stamped into every wal record so the log data is self describing and
	// needs no directory or catalog context to attribute a line.
	OutputDeployment int32
	OutputNode       int32
	OutputOrdinal    int32

	// NetnsPath joins the container to a pre-created network namespace (the
	// virtual network). Empty = host networking.
	NetnsPath string
	// ResolvConfPath is a generated resolv.conf to bind-mount read-only when
	// NetnsPath is set (points at the machine's netproxy DNS). Empty = the
	// host's resolv.conf.
	ResolvConfPath string
}

// ImageStream is an OCI/Docker image tar stream to import into containerd.
type ImageStream struct {
	Reader io.Reader
	Ref    string // image ref to create/update in containerd
}

// ExitStatus is the terminal result of a container task.
type ExitStatus struct {
	Code uint32
	Err  error
}
