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

// --- String methods for status enums ---

func (s RunningStatus) String() string {
	switch s {
	case RunningStatus_DEPLOYMENT_STATUS_UNKNOWN:
		return "UNKNOWN"
	case RunningStatus_NO_DEPLOYMENT:
		return "NO_DEPLOYMENT"
	case RunningStatus_RUNNING:
		return "RUNNING"
	case RunningStatus_STOPPED:
		return "STOPPED"
	case RunningStatus_STARTING:
		return "STARTING"
	case RunningStatus_CRASHED:
		return "CRASHED"
	default:
		return fmt.Sprintf("RunningStatus(%d)", int32(s))
	}
}

func (s PreparationStatus) String() string {
	switch s {
	case PreparationStatus_PREPARATION_STATUS_UNKNOWN:
		return "UNKNOWN"
	case PreparationStatus_PREPARING:
		return "PREPARING"
	case PreparationStatus_DOWNLOADING:
		return "DOWNLOADING"
	case PreparationStatus_READY:
		return "READY"
	case PreparationStatus_FAILED:
		return "FAILED"
	default:
		return fmt.Sprintf("PreparationStatus(%d)", int32(s))
	}
}

func (s AccessPolicyType) String() string {
	switch s {
	case AccessPolicyType_ACCESS_POLICY_TYPE_UNSPECIFIED:
		return "UNSPECIFIED"
	case AccessPolicyType_NO_AUTH:
		return "NO_AUTH"
	case AccessPolicyType_OPTIONAL_AUTH:
		return "OPTIONAL_AUTH"
	case AccessPolicyType_ANY_OF:
		return "ANY_OF"
	default:
		return fmt.Sprintf("AccessPolicyType(%d)", int32(s))
	}
}
