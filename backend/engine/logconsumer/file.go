package logconsumer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/pkg/cio"
)

type FileMode int

const (
	Append FileMode = iota
	Truncate
)

// NewFile returns a containerd IO creator that asks the shim/runtime to write
// stdout and stderr directly to path. The file is created up front so OpenDeploy
// controls directory permissions and fresh-start truncation; containerd appends
// to the resulting file URI.
func NewFile(path string, mode FileMode) (cio.Creator, error) {
	if path == "" {
		return nil, fmt.Errorf("log path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if mode == Truncate {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening log file %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("closing log file %q: %w", path, err)
	}
	return cio.LogFile(path), nil
}
