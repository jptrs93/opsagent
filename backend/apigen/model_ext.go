package apigen

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

// Bumped to 13 when the api-contract was rewritten from the data model: every
// wire shape changed, so an older node must be refused at the hello.
const ClusterProtocolVersion uint32 = 15

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
func (s *ScheduledInstanceStatus) BumpUpdatedAt() {
	s.UpdatedAt = Maybe[time.Time]{Value: nextObservationTime(s.UpdatedAt.Value), Present: true}
}

func (s *NodeStatus) BumpUpdatedAt() {
	s.UpdatedAt = Maybe[time.Time]{Value: nextObservationTime(s.UpdatedAt.Value), Present: true}
}

func (s *ScheduledInstanceStatus) UpdatedAtTime() time.Time { return s.UpdatedAt.Value }
func (s *NodeStatus) UpdatedAtTime() time.Time              { return s.UpdatedAt.Value }

func nextObservationTime(previous time.Time) time.Time {
	now := time.Now().Round(0)
	previous = previous.Round(0)
	if now.After(previous) {
		return now
	}
	return previous.Add(time.Nanosecond)
}

func prepareOutputFile(deploymentID uint64, version uint32) string {
	return filepath.Join(ainit.StaticConfig.PrepareOutputDir, fmt.Sprintf("%d", deploymentID), fmt.Sprintf("%d.log", version))
}

func LogWALDeploymentDir(deploymentID uint64) string {
	return filepath.Join(ainit.StaticConfig.LogWALDir, fmt.Sprintf("%d", deploymentID))
}

func (d *DeploymentRecord) PrepareOutputPath() string {
	return prepareOutputFile(d.Deployment.ID, d.Meta.SpecVersion)
}

func (d *DeploymentRecord) WorkloadVersion() string {
	return d.Deployment.Spec.WorkloadVersion()
}

func (d *DeploymentRecord) WorkloadRunning() bool {
	return d.Deployment.Scheduling.Running
}

func (d *DeploymentRecord) Deleted() bool {
	return d.Meta.Deleted
}

func (d *DeploymentRecord) EffectiveUpgradeStrategy() ContainerUpgradeStrategy {
	container := d.Deployment.Spec.Container()
	if container == nil || container.UpgradeStrategy == ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		return ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE
	}
	return container.UpgradeStrategy
}

func (d *DeploymentRecord) SetWorkloadState(version string, running bool) error {
	if err := d.Deployment.Spec.SetWorkloadVersion(version); err != nil {
		return err
	}
	d.Deployment.Scheduling.Running = running
	return nil
}

// PlacementNodeID is the single dedicated node a deployment runs on, or zero
// while it has none. Multi-node placements will need callers to iterate the
// node list instead.
func (d *Deployment) PlacementNodeID() uint64 {
	nodes := d.Scheduling.Placement.Value.DedicatedNodes
	if nodes == nil || len(nodes.Nodes) == 0 {
		return 0
	}
	return nodes.Nodes[0]
}

func (d *Deployment) Running() bool {
	return d.Scheduling.Running
}

func DedicatedScheduling(running bool, nodes ...uint64) Scheduling {
	return Scheduling{Running: running, Placement: Placement{Value: PlacementValueOneof{DedicatedNodes: &DedicatedNodesScheduling{Nodes: nodes}}}}
}

func (s *DeploymentSpec) Container() *ContainerSpec {
	return s.Workload.Value.Container
}

func (s *DeploymentSpec) WorkloadVersion() string {
	if container := s.Container(); container != nil {
		return container.Version
	}
	return ""
}

func (s *DeploymentSpec) SetWorkloadVersion(version string) error {
	if container := s.Container(); container != nil {
		container.Version = version
		return nil
	}
	return fmt.Errorf("deployment spec has no supported workload")
}

func (r *PrepareOutputRequest) OutputPath() string {
	return prepareOutputFile(r.DeploymentID, r.SpecVersion)
}

func (s RunningStatus) String() string {
	switch s {
	case RunningStatus_RUNNING_STATUS_UNSPECIFIED:
		return "UNKNOWN"
	case RunningStatus_RUNNING_STATUS_NO_DEPLOYMENT:
		return "NO_DEPLOYMENT"
	case RunningStatus_RUNNING_STATUS_RUNNING:
		return "RUNNING"
	case RunningStatus_RUNNING_STATUS_STOPPED:
		return "STOPPED"
	case RunningStatus_RUNNING_STATUS_STARTING:
		return "STARTING"
	case RunningStatus_RUNNING_STATUS_CRASHED:
		return "CRASHED"
	default:
		return fmt.Sprintf("RunningStatus(%d)", int32(s))
	}
}

func (s PreparationStatus) String() string {
	switch s {
	case PreparationStatus_PREPARATION_STATUS_UNSPECIFIED:
		return "UNKNOWN"
	case PreparationStatus_PREPARATION_STATUS_PREPARING:
		return "PREPARING"
	case PreparationStatus_PREPARATION_STATUS_DOWNLOADING:
		return "DOWNLOADING"
	case PreparationStatus_PREPARATION_STATUS_READY:
		return "READY"
	case PreparationStatus_PREPARATION_STATUS_FAILED:
		return "FAILED"
	case PreparationStatus_PREPARATION_STATUS_PULLING:
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
// already-prepared instance from demoting its rollup and stopping its runner:
// the artifact is built, only input distribution failed.
func (p PreparerStatus) Rollup() PreparationStatus {
	image := p.Image.Value
	if !p.Image.Present {
		image = ImageStatus_IMAGE_STATUS_UNSPECIFIED
	}
	if p.Inputs == InputsStatus_INPUTS_STATUS_FAILED || image == ImageStatus_IMAGE_STATUS_FAILED {
		return PreparationStatus_PREPARATION_STATUS_FAILED
	}
	switch image {
	case ImageStatus_IMAGE_STATUS_READY:
		return PreparationStatus_PREPARATION_STATUS_READY
	case ImageStatus_IMAGE_STATUS_PULLING:
		return PreparationStatus_PREPARATION_STATUS_PULLING
	case ImageStatus_IMAGE_STATUS_DOWNLOADING:
		return PreparationStatus_PREPARATION_STATUS_DOWNLOADING
	case ImageStatus_IMAGE_STATUS_BUILDING:
		return PreparationStatus_PREPARATION_STATUS_PREPARING
	}
	if p.Inputs != InputsStatus_INPUTS_STATUS_UNSPECIFIED {
		return PreparationStatus_PREPARATION_STATUS_PREPARING
	}
	return PreparationStatus_PREPARATION_STATUS_UNSPECIFIED
}

// ImageStage is the image stage with absence read as unspecified.
func (p PreparerStatus) ImageStage() ImageStatus {
	if !p.Image.Present {
		return ImageStatus_IMAGE_STATUS_UNSPECIFIED
	}
	return p.Image.Value
}

func (p *PreparerStatus) SetImage(s ImageStatus) {
	p.Image = Maybe[ImageStatus]{Value: s, Present: true}
}

func (s InputsStatus) String() string {
	switch s {
	case InputsStatus_INPUTS_STATUS_UNSPECIFIED:
		return "UNKNOWN"
	case InputsStatus_INPUTS_STATUS_RESOLVING:
		return "RESOLVING"
	case InputsStatus_INPUTS_STATUS_READY:
		return "READY"
	case InputsStatus_INPUTS_STATUS_FAILED:
		return "FAILED"
	default:
		return fmt.Sprintf("InputsStatus(%d)", int32(s))
	}
}

func (s ImageStatus) String() string {
	switch s {
	case ImageStatus_IMAGE_STATUS_UNSPECIFIED:
		return "UNKNOWN"
	case ImageStatus_IMAGE_STATUS_BUILDING:
		return "BUILDING"
	case ImageStatus_IMAGE_STATUS_PULLING:
		return "PULLING"
	case ImageStatus_IMAGE_STATUS_DOWNLOADING:
		return "DOWNLOADING"
	case ImageStatus_IMAGE_STATUS_READY:
		return "READY"
	case ImageStatus_IMAGE_STATUS_FAILED:
		return "FAILED"
	default:
		return fmt.Sprintf("ImageStatus(%d)", int32(s))
	}
}

func (k UserSessionKind) String() string {
	switch k {
	case UserSessionKind_USER_SESSION_KIND_FULL:
		return "FULL"
	case UserSessionKind_USER_SESSION_KIND_BOOTSTRAP:
		return "BOOTSTRAP"
	default:
		return fmt.Sprintf("UserSessionKind(%d)", int32(k))
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

// RunningVersion is the workload version the runner half of a status is
// running: the pinned deployment's version when the runner reports that
// deployment's spec version, otherwise empty.
func RunningVersion(cfg *DeploymentRecord, st ScheduledInstanceStatus) string {
	if cfg == nil || !st.Runner.Present {
		return ""
	}
	ver := st.Runner.Value.DeploymentSpecVersion
	if ver == 0 || ver != cfg.Meta.SpecVersion {
		return ""
	}
	return cfg.WorkloadVersion()
}

func (u *CoreWriteUpdate) IsEmpty() bool {
	return u == nil || len(u.Mutations) == 0
}

// Kind returns the mutation kind as the matching AuthzVerb.
func (m *CoreMutation) Kind() AuthzVerb {
	switch {
	case m == nil:
		return AuthzVerb_AUTHZ_VERB_UNSPECIFIED
	case m.Value.Delete != nil:
		return AuthzVerb_AUTHZ_VERB_DELETE
	case m.Value.Create != nil:
		return AuthzVerb_AUTHZ_VERB_CREATE
	default:
		return AuthzVerb_AUTHZ_VERB_UPDATE
	}
}

func (m *CoreMutation) Type() CoreEntityType {
	switch {
	case m == nil:
		return CoreEntityType_CORE_ENTITY_UNSPECIFIED
	case m.Value.Create != nil:
		return m.Value.Create.EntityType
	case m.Value.Update != nil:
		return m.Value.Update.EntityType
	case m.Value.Delete != nil:
		return m.Value.Delete.EntityType
	}
	return CoreEntityType_CORE_ENTITY_UNSPECIFIED
}

func (m *CoreMutation) EntityID() uint64 {
	switch {
	case m == nil:
		return 0
	case m.Value.Create != nil:
		return m.Value.Create.EntityID
	case m.Value.Update != nil:
		return m.Value.Update.EntityID
	case m.Value.Delete != nil:
		return m.Value.Delete.EntityID
	}
	return 0
}

// Entity returns the payload of a create or update, and nil for a delete.
func (m *CoreMutation) Entity() *CoreEntity {
	switch {
	case m == nil:
		return nil
	case m.Value.Create != nil:
		return &m.Value.Create.Entity
	case m.Value.Update != nil:
		return &m.Value.Update.Entity
	}
	return nil
}

// Meta returns what the log says about the entity after a create or update,
// and nil for a delete or before the reducer stamped it.
func (m *CoreMutation) Meta() *EntityMeta {
	var meta *Maybe[EntityMeta]
	switch {
	case m == nil:
		return nil
	case m.Value.Create != nil:
		meta = &m.Value.Create.Meta
	case m.Value.Update != nil:
		meta = &m.Value.Update.Meta
	default:
		return nil
	}
	if !meta.Present {
		return nil
	}
	return &meta.Value
}

func (m *CoreMutation) SetMeta(meta EntityMeta) {
	switch {
	case m == nil:
	case m.Value.Create != nil:
		m.Value.Create.Meta = Maybe[EntityMeta]{Value: meta, Present: true}
	case m.Value.Update != nil:
		m.Value.Update.Meta = Maybe[EntityMeta]{Value: meta, Present: true}
	}
}

func CreateMutationOf(t CoreEntityType, id uint64, entity CoreEntity) CoreMutation {
	return CoreMutation{Value: CoreMutationValueOneof{Create: &CreateMutation{EntityType: t, EntityID: id, Entity: entity}}}
}

func UpdateMutationOf(t CoreEntityType, id uint64, entity CoreEntity) CoreMutation {
	return CoreMutation{Value: CoreMutationValueOneof{Update: &UpdateMutation{EntityType: t, EntityID: id, Entity: entity}}}
}

func DeleteMutationOf(t CoreEntityType, id uint64) CoreMutation {
	return CoreMutation{Value: CoreMutationValueOneof{Delete: &DeleteMutation{EntityType: t, EntityID: id}}}
}

// Has reports whether the update carries a mutation of the given type.
func (u *CoreWriteUpdate) Has(t CoreEntityType) bool {
	if u == nil {
		return false
	}
	for _, m := range u.Mutations {
		if m.Type() == t {
			return true
		}
	}
	return false
}

func Some[T any](v T) Maybe[T] { return Maybe[T]{Value: v, Present: true} }

func TimeOf(t time.Time) Maybe[time.Time] {
	if t.IsZero() {
		return Maybe[time.Time]{}
	}
	return Maybe[time.Time]{Value: t, Present: true}
}
