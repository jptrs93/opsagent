package legacyconv

import (
	"encoding/json"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func ScheduledInstance(old *apigenold.ScheduledInstance) (*apigen.ScheduledInstance, error) {
	c := &conv{}
	if old == nil {
		c.refuse("ScheduledInstance", "", "nil payload")
		return checked("ScheduledInstance", (*apigen.ScheduledInstance)(nil), c.err)
	}
	return checked("ScheduledInstance", c.scheduledInstance(old), c.err)
}

func (c *conv) scheduledInstance(old *apigenold.ScheduledInstance) *apigen.ScheduledInstance {
	return &apigen.ScheduledInstance{
		ID:              c.id("ScheduledInstance", "id", int64(old.ID)),
		NodeID:          c.id("ScheduledInstance", "node_id", int64(old.NodeID)),
		InstanceOrdinal: c.u32("ScheduledInstance", "instance_ordinal", int64(old.InstanceOrdinal)),
		State:           apigen.ScheduledInstanceTarget(old.State),
		SpaceID:         c.id("ScheduledInstance", "space_id", int64(old.SpaceID)),
		Deployment: apigen.DeploymentRef{
			DeploymentID: c.id("ScheduledInstance", "deployment_id", int64(old.DeploymentID)),
			Version:      c.u32("ScheduledInstance", "deployment_version", int64(old.DeploymentVersion)),
		},
	}
}

func ScheduledInstanceStatus(old *apigenold.ScheduledInstanceStatus) (*apigen.ScheduledInstanceStatus, error) {
	c := &conv{}
	if old == nil {
		c.refuse("ScheduledInstanceStatus", "", "nil payload")
		return checked("ScheduledInstanceStatus", (*apigen.ScheduledInstanceStatus)(nil), c.err)
	}
	return checked("ScheduledInstanceStatus", c.scheduledInstanceStatus(old), c.err)
}

func (c *conv) scheduledInstanceStatus(old *apigenold.ScheduledInstanceStatus) *apigen.ScheduledInstanceStatus {
	out := &apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: c.id("ScheduledInstanceStatus", "scheduled_instance_id", int64(old.ScheduledInstanceID)),
		UpdatedAt:           apigen.TimeOf(old.UpdatedAt),
	}
	if !old.Preparer.IsZero() {
		p := old.Preparer
		ps := apigen.PreparerStatus{
			DeploymentSpecVersion: c.u32("PreparerStatus", "deployment_spec_version", int64(p.DeploymentSpecVersion)),
			Artifact:              p.Artifact,
			Inputs:                apigen.InputsStatus(p.Inputs),
		}
		if p.Image != 0 {
			ps.Image = apigen.Some(apigen.ImageStatus(p.Image))
		}
		out.Preparer = apigen.Some(ps)
	}
	if !old.Runner.IsZero() {
		r := old.Runner
		rs := apigen.RunnerStatus{
			DeploymentSpecVersion: c.u32("RunnerStatus", "deployment_spec_version", int64(r.DeploymentSpecVersion)),
			RunningPid:            c.optU32("RunnerStatus", "running_pid", r.RunningPid),
			RunningArtifact:       r.RunningArtifact,
			Status:                apigen.RunningStatus(r.Status),
			NumberOfRestarts:      c.u32("RunnerStatus", "number_of_restarts", int64(r.NumberOfRestarts)),
			LastRestartAt:         apigen.TimeOf(r.LastRestartAt),
			NetworkDiagnostics:    r.NetworkDiagnostics,
		}
		if r.ExitCode != nil {
			rs.ExitCode = apigen.Some(*r.ExitCode)
		}
		out.Runner = apigen.Some(rs)
	}
	return out
}

// ScheduledInstanceState converts the secondary's cached instance state: the
// instance, the pinned deployment record (the old event's version counters,
// seq, times, author, and event type become its EntityMeta), and the status
// when the old record carried one.
func ScheduledInstanceState(old []byte) (*apigen.ScheduledInstanceState, error) {
	decoded, err := apigenold.DecodeScheduledInstanceState(old)
	if err != nil {
		return nil, fmt.Errorf("ScheduledInstanceState: decode: %w", err)
	}
	c := &conv{}
	out := &apigen.ScheduledInstanceState{
		Instance: *c.scheduledInstance(&decoded.Instance),
		Config:   c.deploymentRecord(&decoded.Config),
	}
	if !decoded.Status.IsZero() {
		out.Status = apigen.Some(*c.scheduledInstanceStatus(&decoded.Status))
	}
	return checked("ScheduledInstanceState", out, c.err)
}

func (c *conv) deploymentRecord(old *apigenold.DeploymentEvent) apigen.DeploymentRecord {
	dep := c.deployment(&old.Value)
	if dep.ID == 0 {
		dep.ID = c.id("DeploymentEvent", "deployment_id", int64(old.DeploymentID))
	}
	return apigen.DeploymentRecord{Deployment: *dep, Meta: apigen.EntityMeta{
		CreatedTime:  millisOf(old.CreatedTime),
		UpdatedTime:  millisOf(old.EventTime),
		UpdatedSeq:   old.Seq,
		UpdatedActor: int64(old.Author),
		Version:      c.u32("DeploymentEvent", "version", int64(old.Version)),
		SpecVersion:  c.u32("DeploymentEvent", "spec_version", int64(old.SpecVersion)),
		Deleted:      old.EventType == apigenold.EventType_EVENT_TYPE_DELETE,
	}}
}

// RunnerStatusExtra turns the old encoded RunnerStatus extra blob of a status
// row into the JSON string list the new rows keep; empty in, empty out.
func RunnerStatusExtra(old []byte) ([]byte, error) {
	if len(old) == 0 {
		return nil, nil
	}
	rs, err := apigenold.DecodeRunnerStatus(old)
	if err != nil {
		return nil, fmt.Errorf("RunnerStatus: decode extra blob: %w", err)
	}
	if len(rs.NetworkDiagnostics) == 0 {
		return nil, nil
	}
	return json.Marshal(rs.NetworkDiagnostics)
}
