package apigen

import (
	"fmt"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func prepareOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.Config.PrepareOutputDir, fmt.Sprintf("%d_%d", deploymentID, version))
}

func RunOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.Config.RunOutputDir, fmt.Sprintf("%d_%d", deploymentID, version))
}

func (d *DeploymentConfig) PrepareOutputPath() string {
	return prepareOutputFile(d.ID, d.Version)
}

func (d *DeploymentConfig) RunOutputPath() string {
	return RunOutputFile(d.ID, d.Version)
}

func (r *PrepareOutputRequest) OutputPath() string {
	return prepareOutputFile(r.DeploymentID, r.Version)
}

func (r *RunOutputRequest) OutputPath() string {
	return RunOutputFile(r.DeploymentID, r.Version)
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
