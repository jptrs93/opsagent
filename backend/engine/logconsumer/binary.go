package logconsumer

import (
	"os"

	"github.com/containerd/containerd/v2/pkg/cio"
	processlog "github.com/jptrs93/opsagent/backend/logconsumer"
)

// NewBinaryV2 returns a containerd logging URI creator that starts this same
// opendeploy binary in v2 log-consumer mode. Containerd names this URI scheme
// "binary"; the child process uses runtime/v2/logging.Run for the protocol.
// A single query key is used so the shim invokes: opendeploy log-consumer <basePath>.
func NewBinaryV2(basePath string) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{processlog.CommandName: basePath})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}
