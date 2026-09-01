package apigen

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// Bumped to 7 when assets split into assets + asset_versions: the cluster
// asset fetch renamed its query param and headers to asset_version_id naming.
// Bumped to 8 when issued TLS mounts were added: an older secondary would run an
// issued-TLS deployment without its cert material.
// Bumped to 10 when the streaming log search was replaced by the one-shot
// structured log query round trip (per-field stats ride in its response).
const ClusterProtocolVersion int32 = 10

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

// BumpUpdatedAt advances UpdatedAt as a hybrid logical clock: it takes the
// current wall clock, but never returns a value <= the previous one (it adds
// a nanosecond instead). This keeps the value monotonic per scheduled instance
// across clock regressions and same-tick writes, while tracking physical time
// closely enough that a node which lost its local state (e.g. a freshly
// provisioned replacement) resumes above any history the primary retained,
// with no reseed handshake. UpdatedAt thus serves as both the status's
// wall-clock time and its monotonic identity/ordering key.
func (s *ScheduledInstanceStatus) BumpUpdatedAt() {
	now := time.Now().Round(0)
	previous := s.UpdatedAt.Round(0)
	if now.After(previous) {
		s.UpdatedAt = now
	} else {
		s.UpdatedAt = previous.Add(time.Nanosecond)
	}
}

func prepareOutputFile(deploymentID int32, version int32) string {
	return filepath.Join(ainit.StaticConfig.PrepareOutputDir, fmt.Sprintf("%d", deploymentID), fmt.Sprintf("%d.log", version))
}

func LogWALDeploymentDir(deploymentID int32) string {
	return filepath.Join(ainit.StaticConfig.LogWALDir, fmt.Sprintf("%d", deploymentID))
}

func (d *Deployment) PrepareOutputPath() string {
	return prepareOutputFile(d.ID, d.SpecVersion)
}

func (d *Deployment) WorkloadVersion() string {
	return d.Spec.WorkloadVersion()
}

func (d *Deployment) WorkloadRunning() bool {
	return d.Spec.WorkloadRunning()
}

func (d *Deployment) EffectiveUpgradeStrategy() ContainerUpgradeStrategy {
	container := d.Spec.Container()
	if container == nil || container.UpgradeStrategy == ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		return ContainerUpgradeStrategy_RECREATE
	}
	return container.UpgradeStrategy
}

func (d *Deployment) SetWorkloadState(version string, running bool) error {
	return d.Spec.SetWorkloadState(version, running)
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

// SpaceID is the asset's current space: the newest entry of the append-only
// space log.
func (a *Asset) SpaceID() int32 {
	if a == nil || len(a.SpaceVersions) == 0 {
		return 0
	}
	return a.SpaceVersions[0].SpaceID
}

// LatestContentVersion is the newest content version, or nil for an asset
// with no published version (never surfaced by list/get reads).
func (a *Asset) LatestContentVersion() *AssetContentVersion {
	if a == nil || len(a.ContentVersions) == 0 {
		return nil
	}
	return a.ContentVersions[0]
}

// SpaceID is the config's current space: the newest entry of the append-only
// space log.
func (c *Config) SpaceID() int32 {
	if c == nil || len(c.SpaceVersions) == 0 {
		return 0
	}
	return c.SpaceVersions[0].SpaceID
}

// LatestValueVersion is the newest value version, or nil for a config with no
// version (never surfaced by list/get reads).
func (c *Config) LatestValueVersion() *ConfigValueVersion {
	if c == nil || len(c.ValueVersions) == 0 {
		return nil
	}
	return c.ValueVersions[0]
}

// SpaceID is the secret's current space: the newest entry of the append-only
// space log.
func (s *Secret) SpaceID() int32 {
	if s == nil || len(s.SpaceVersions) == 0 {
		return 0
	}
	return s.SpaceVersions[0].SpaceID
}

// LatestVersion is the newest version, or nil for a secret with no version
// (never surfaced by list/get reads).
func (s *Secret) LatestVersion() *SecretVersion {
	if s == nil || len(s.Versions) == 0 {
		return nil
	}
	return s.Versions[0]
}
