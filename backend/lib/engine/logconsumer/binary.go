package logconsumer

import (
	"os"

	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/jptrs93/opsagent/backend/ainit"
	processlog "github.com/jptrs93/opsagent/backend/app/logconsumer"
)

// NewRawBinaryV2 returns a containerd logging URI creator that starts this same
// opendeploy binary in v2 raw-binary-log-consumer mode.
func NewRawBinaryV2(deploymentDir string, version int32, run int32) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	config, err := processlog.RawBinaryConfigArg(deploymentDir, version, run)
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{string(ainit.CommandRawLogConsumer): config})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}
