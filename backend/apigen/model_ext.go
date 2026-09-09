package apigen

import (
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// Bumped to 7 when assets split into assets + asset_versions: the cluster
// asset fetch renamed its query param and headers to asset_version_id naming.
// Bumped to 8 when issued TLS mounts were added: an older secondary would run an
// issued-TLS deployment without its cert material.
// Bumped to 10 when the streaming log search was replaced by the one-shot
// structured log query round trip (per-field stats ride in its response).
// Bumped to 11 when the metrics query and latest-sample round trips were added.
const ClusterProtocolVersion int32 = 11

// WantsRunning reports whether a node should be running this placement. The
// three RUN_* states are deliberately indistinguishable here: they differ only
// in what cross-node routing derives from them, never in what the operator
// does. Every target-state check in the engine must go through this rather than
// comparing against RUN_SERVING, or a standby placement silently never starts.
func (t ScheduledInstanceTarget) WantsRunning() bool {
	switch t {
	case ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY,
		ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING:
		return true
	default:
		return false
	}
}

// IsFinal reports whether the primary has accepted that this placement is gone.
func (t ScheduledInstanceTarget) IsFinal() bool {
	return t == ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
}

// BumpUpdatedAt advances the observation clock past both wall time and the
// previous persisted observation, including a restored tombstone.
func (s *ScheduledInstanceStatus) BumpUpdatedAt() { s.UpdatedAt = nextObservationTime(s.UpdatedAt) }
func (s *NodeStatus) BumpUpdatedAt()              { s.UpdatedAt = nextObservationTime(s.UpdatedAt) }

func nextObservationTime(previous time.Time) time.Time {
	now := time.Now().Round(0)
	previous = previous.Round(0)
	if now.After(previous) {
		return now
	}
	return previous.Add(time.Nanosecond)
}

func prepareOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.StaticConfig.PrepareOutputDir, fmt.Sprintf("%d", deploymentID), fmt.Sprintf("%d.log", version))
}

func LogWALDeploymentDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.LogWALDir, fmt.Sprintf("%d", deploymentID))
}

func (d *DeploymentEvent) PrepareOutputPath() string {
	return prepareOutputFile(d.DeploymentID, d.SpecVersion)
}

func (d *DeploymentEvent) WorkloadVersion() string {
	return d.Value.Spec.WorkloadVersion()
}

func (d *DeploymentEvent) WorkloadRunning() bool {
	return d.Value.Spec.WorkloadRunning()
}

func (d *DeploymentEvent) EffectiveUpgradeStrategy() ContainerUpgradeStrategy {
	container := d.Value.Spec.Container()
	if container == nil || container.UpgradeStrategy == ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		return ContainerUpgradeStrategy_RECREATE
	}
	return container.UpgradeStrategy
}

func (d *DeploymentEvent) SetWorkloadState(version string, running bool) error {
	return d.Value.Spec.SetWorkloadState(version, running)
}

func (d *DeploymentEvent) Deleted() bool {
	return d.EventType == EventType_EVENT_TYPE_DELETE
}

func (s *DeploymentSpec) WorkloadVersion() string {
	if container := s.Container(); container != nil {
		return container.Version
	}
	if s.OpendeploySpec != nil {
		return s.OpendeploySpec.Version
	}
	return ""
}

func (s *DeploymentSpec) WorkloadRunning() bool {
	if container := s.Container(); container != nil {
		return container.Running
	}
	return s.OpendeploySpec != nil
}

func (s *DeploymentSpec) SetWorkloadState(version string, running bool) error {
	if container := s.Container(); container != nil {
		container.Version = version
		container.Running = running
		return nil
	}
	if s.OpendeploySpec != nil {
		s.OpendeploySpec.Version = version
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
	return prepareOutputFile(r.DeploymentID, r.SpecVersion)
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
	case PreparationStatus_PULLING:
		return "PULLING"
	default:
		return fmt.Sprintf("PreparationStatus(%d)", int32(s))
	}
}

// Rollup collapses the two preparation stages into the single status that gates
// runner start, holds a prepare-log stream open, and marks an instance
// quiescent. It is derived rather than stored, so the stages can never disagree
// with it.
//
// Note the ordering: an image that is already READY wins over an inputs stage
// that is merely resolving. That is what keeps an input retry on an
// already-prepared instance from demoting its rollup and stopping its runner —
// the artifact is built, only input distribution failed.
func (p PreparerStatus) Rollup() PreparationStatus {
	if p.Inputs == InputsStatus_INPUTS_FAILED || p.Image == ImageStatus_IMAGE_FAILED {
		return PreparationStatus_FAILED
	}
	switch p.Image {
	case ImageStatus_IMAGE_READY:
		return PreparationStatus_READY
	case ImageStatus_IMAGE_PULLING:
		return PreparationStatus_PULLING
	case ImageStatus_IMAGE_DOWNLOADING:
		return PreparationStatus_DOWNLOADING
	case ImageStatus_IMAGE_BUILDING:
		return PreparationStatus_PREPARING
	}
	// The image stage has not started. Anything past the start of stage 1 still
	// reads as PREPARING to everything downstream.
	if p.Inputs != InputsStatus_INPUTS_STATUS_UNKNOWN {
		return PreparationStatus_PREPARING
	}
	return PreparationStatus_PREPARATION_STATUS_UNKNOWN
}

func (s InputsStatus) String() string {
	switch s {
	case InputsStatus_INPUTS_STATUS_UNKNOWN:
		return "UNKNOWN"
	case InputsStatus_INPUTS_RESOLVING:
		return "RESOLVING"
	case InputsStatus_INPUTS_READY:
		return "READY"
	case InputsStatus_INPUTS_FAILED:
		return "FAILED"
	default:
		return fmt.Sprintf("InputsStatus(%d)", int32(s))
	}
}

func (s ImageStatus) String() string {
	switch s {
	case ImageStatus_IMAGE_STATUS_UNKNOWN:
		return "UNKNOWN"
	case ImageStatus_IMAGE_BUILDING:
		return "BUILDING"
	case ImageStatus_IMAGE_PULLING:
		return "PULLING"
	case ImageStatus_IMAGE_DOWNLOADING:
		return "DOWNLOADING"
	case ImageStatus_IMAGE_READY:
		return "READY"
	case ImageStatus_IMAGE_FAILED:
		return "FAILED"
	default:
		return fmt.Sprintf("ImageStatus(%d)", int32(s))
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

func (v *SecretEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}
func (v *ConfigEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}
func (v *AssetEvent) SpaceID() int32 {
	if v == nil {
		return 0
	}
	return v.Value.SpaceID
}

// ReportedValue accepts the previous release's flat hello during worker rollout.
func (h *EnrollmentHello) ReportedValue() NodeReported {
	if h.Reported == nil {
		return NodeReported{}
	}
	return *h.Reported
}

func (h *ClusterHello) ReportedValue() NodeReported {
	if h.Reported == nil {
		return NodeReported{}
	}
	return *h.Reported
}

func WithRunningVersion(cfg *DeploymentEvent, st ScheduledInstanceStatus) ScheduledInstanceStatus {
	if st.Runner.IsZero() || cfg == nil {
		return st
	}
	ver := st.Runner.DeploymentSpecVersion
	if ver == 0 {
		return st
	}
	if ver == cfg.SpecVersion {
		st.Runner.RunningVersion = cfg.WorkloadVersion()
	}
	return st
}

func (u *CoreUpdate) IsEmpty() bool {
	value := reflect.ValueOf(u).Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Type().Field(i).Name == "Seq" {
			continue
		}
		field := value.Field(i)
		switch field.Kind() {
		case reflect.Slice, reflect.Map:
			if field.Len() > 0 {
				return false
			}
		default:
			if !field.IsZero() {
				return false
			}
		}
	}
	return true
}

func (u *CoreUpdate) HasObserved() bool {
	return len(u.InstanceStatuses)+len(u.NodeStatuses) > 0
}

func (u *CoreUpdate) HasCore() bool {
	authored := *u
	authored.InstanceStatuses, authored.NodeStatuses = nil, nil
	return !authored.IsEmpty()
}
