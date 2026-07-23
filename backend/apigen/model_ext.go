package apigen

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

const ClusterProtocolVersion int32 = 2

// BumpUpdatedAt advances UpdatedAt as a hybrid logical clock: it takes the
// current wall clock, but never returns a value <= the previous one (it adds
// a nanosecond instead). This keeps the value monotonic per deployment across
// clock regressions and same-tick writes, while tracking physical time closely
// enough that a node which lost its local state (e.g. a freshly provisioned
// replacement) resumes above any history the primary retained, with no reseed
// handshake. UpdatedAt thus serves as both the status's wall-clock time and
// its monotonic identity/ordering key.
func (s *DeploymentStatus) BumpUpdatedAt() {
	if now := time.Now(); now.After(s.UpdatedAt) {
		s.UpdatedAt = now
	} else {
		s.UpdatedAt = s.UpdatedAt.Add(time.Nanosecond)
	}
}

func prepareOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.StaticConfig.PrepareOutputDir, fmt.Sprintf("%d", deploymentID), fmt.Sprintf("%d.log", version))
}

func RunOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.StaticConfig.RunOutputDir, fmt.Sprintf("%d", deploymentID), fmt.Sprintf("%d.log", version))
}

func RunOutputDeploymentDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.RunOutputDir, fmt.Sprintf("%d", deploymentID))
}

func (d *DeploymentConfig) PrepareOutputPath() string {
	return prepareOutputFile(d.ID, d.Version)
}

func (d *DeploymentConfig) RunOutputPath() string {
	return RunOutputFile(d.ID, d.Version)
}

func (d *DeploymentConfig) WorkloadVersion() string {
	return d.Spec.WorkloadVersion()
}

func (d *DeploymentConfig) WorkloadRunning() bool {
	return d.Spec.WorkloadRunning()
}

func (d *DeploymentConfig) SetWorkloadState(version string, running bool) error {
	return d.Spec.SetWorkloadState(version, running)
}

func (s *DeploymentSpec) WorkloadVersion() string {
	if container := s.Container(); container != nil {
		return container.Version
	}
	if s.SystemdSpec != nil {
		return s.SystemdSpec.Version
	}
	return ""
}

func (s *DeploymentSpec) WorkloadRunning() bool {
	if container := s.Container(); container != nil {
		return container.Running
	}
	return s.SystemdSpec != nil && s.SystemdSpec.Running
}

func (s *DeploymentSpec) SetWorkloadState(version string, running bool) error {
	if container := s.Container(); container != nil {
		container.Version = version
		container.Running = running
		return nil
	}
	if s.SystemdSpec != nil {
		s.SystemdSpec.Version = version
		s.SystemdSpec.Running = running
		return nil
	}
	return fmt.Errorf("deployment spec has no supported workload")
}

func (s *DeploymentSpec) Container() *ContainerSpec {
	for _, container := range []*ContainerSpec{s.Container1Spec, s.Container2Spec, s.Container3Spec} {
		if container != nil {
			return container
		}
	}
	return nil
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
