package apigen

import (
	"fmt"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// prepareOutputFile is the canonical on-disk name for a preparation's log
// file. Keyed by environment, deployment name, and the deployment SeqNo the
// preparation was run for. Machine is not part of the name because each
// slave has its own data dir.
func prepareOutputFile(id *DeploymentIdentifier, seqNo int32) string {
	return filepath.Join(ainit.Config.PrepareOutputDir, fmt.Sprintf("%v_%v_%v", id.Environment, id.Name, seqNo))
}

// runOutputFile is the canonical on-disk name for a runner's stdout/stderr
// log file. Same keying scheme as prepareOutputFile.
func runOutputFile(id *DeploymentIdentifier, seqNo int32) string {
	return filepath.Join(ainit.Config.RunOutputDir, fmt.Sprintf("%v_%v_%v", id.Environment, id.Name, seqNo))
}

func (d *DeploymentConfig) PrepareOutputPath() string {
	return prepareOutputFile(d.ID, d.SeqNo)
}

func (d *DeploymentConfig) RunOutputPath() string {
	return runOutputFile(d.ID, d.SeqNo)
}

func (d DeploymentStatus) PrepareOutputPath() string {
	return prepareOutputFile(d.DeploymentID, d.Preparer.DeploymentSeqNo)
}

func (d DeploymentStatus) RunOutputPath() string {
	return runOutputFile(d.DeploymentID, d.Runner.DeploymentSeqNo)
}

// OutputPath returns the local prepare log file path for a log request.
// PrepareOutputRequest carries a DeploymentIdentifier + SeqNo so the slave
// can translate a request directly into a file path without touching its
// store.
func (r *PrepareOutputRequest) OutputPath() string {
	return prepareOutputFile(r.ID, r.SeqNo)
}

// OutputPath returns the local run log file path for a log request.
func (r *RunOutputRequest) OutputPath() string {
	return runOutputFile(r.ID, r.SeqNo)
}
