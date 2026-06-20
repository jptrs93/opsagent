// Package ctrd isolates all containerd usage behind a small platform-neutral
// surface. The real implementation (client_linux.go) is compiled only on linux;
// everywhere else a stub (client_other.go) returns ErrUnsupported so the rest of
// opendeploy builds and runs on macOS/dev without containerd.
//
// The Client is the single handle shared by the container image preparer and the
// container runner. It dials containerd lazily, so opendeploy still starts on hosts
// where containerd is absent — only container deployments fail, with a clear
// error.
package ctrd

import (
	"errors"
	"io"
)

// ErrUnsupported is returned by every operation on non-linux platforms.
var ErrUnsupported = errors.New("containers require linux with containerd")

// ErrNotFound is returned by LoadTask when no running container/task exists for
// the given id (the caller should perform a fresh spawn).
var ErrNotFound = errors.New("container task not found")

// Mount is a single host bind mount into the container.
type Mount struct {
	Source   string
	Dest     string
	ReadOnly bool
}

type LogConsumer string

const (
	LogConsumerStandard LogConsumer = "standard"
	LogConsumerJSON     LogConsumer = "json"
)

// ContainerSpec describes a container to create and start. The default data
// volume (if any) is already resolved into Mounts by the caller.
type ContainerSpec struct {
	ID          string      // deterministic container id (one per deployment config version)
	Image       string      // resolved image ref to run (as stored by Pull)
	User        string      // OCI process.user (uid, uid:gid, or name); empty = image default
	Env         []string    // KEY=VALUE entries
	Args        []string    // argv override (entrypoint+cmd); empty = image default
	Cwd         string      // process cwd; empty = image default
	Mounts      []Mount     // host bind mounts
	Output      string      // stdout/stderr log file path
	LogConsumer LogConsumer // defaults to LogConsumerStandard
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
