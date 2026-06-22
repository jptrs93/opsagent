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

// NewRawBinaryV2 returns a containerd logging URI creator that starts this same
// opendeploy binary in v2 raw-binary-log-consumer mode.
func NewRawBinaryV2(basePath string) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{processlog.RawBinaryCommandName: basePath})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}

// NewJSONV2 returns a containerd logging URI creator that starts this same
// opendeploy binary in v2 json-log-consumer mode.
func NewJSONV2(basePath string) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{processlog.JSONCommandName: basePath})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}

// NewOpenObserveV2 returns a containerd logging URI creator that starts this
// same opendeploy binary in v2 openobserve-log-consumer mode.
func NewOpenObserveV2(basePath, openObserveURL, stream, token, saEmail, svc string, version int) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	configPath, err := processlog.WriteOpenObserveConfig(basePath, openObserveURL, stream, token, saEmail, svc, version)
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{processlog.OpenObserveCommandName: configPath})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}

// NewSplitV2 returns a containerd logging URI creator that starts this same
// opendeploy binary in v2 split-log-consumer mode.
func NewSplitV2(basePath string) (cio.Creator, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	uri, err := cio.LogURIGenerator("binary", binary, map[string]string{processlog.SplitCommandName: basePath})
	if err != nil {
		return nil, err
	}
	return cio.LogURI(uri), nil
}
