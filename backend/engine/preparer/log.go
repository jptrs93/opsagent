package preparer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func createPrepareLog(dep *apigen.DeploymentConfig) (*os.File, string, error) {
	logPath := dep.PrepareOutputPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return nil, logPath, fmt.Errorf("creating prepare log dir: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, logPath, err
	}
	return logFile, logPath, nil
}
