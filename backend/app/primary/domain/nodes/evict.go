package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrNodeNotMember = errors.New("node is not a cluster member")
var ErrNodeIsPrimary = errors.New("the primary node cannot be drained or evicted")
var ErrNodeVersionChanged = errors.New("node changed since it was last read")
var ErrEnrollmentIdentifierEvicted = errors.New("identifier was evicted from the cluster")

type ErrNodeHasDeployments struct {
	Count int
}

func (e *ErrNodeHasDeployments) Error() string {
	return fmt.Sprintf("%d deployments still target this node", e.Count)
}

func MemberNodeIDByIdentifier(q *pq.Queries, identifier string) (int32, error) {
	nodeID, err := q.GetNodeIDByIdentifierWithStatus(context.Background(), identifier, pq.MemberNodeStatuses)
	return int32(nodeID), err
}

func IsEvictedIdentifier(q *pq.Queries, identifier string) bool {
	_, err := q.GetNodeIDByIdentifierWithStatus(context.Background(), identifier, []int64{int64(apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED)})
	return err == nil
}

func isPrimaryRow(row pq.CurrentNode) bool {
	return slices.Contains(row.Event.Value.Operator.Roles, NodeRolePrimary)
}

func SetNodeDraining(ctx apigen.Context, store *state.Service, identifier string, draining bool) (*apigen.NodeEvent, error) {
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		if !isMemberStatus(current.Event.Value.Status) {
			return nil, ErrNodeNotMember
		}
		if isPrimaryRow(current) {
			return nil, ErrNodeIsPrimary
		}
		target := apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL
		if draining {
			target = apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING
		} else if current.Event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING {
			row = current
			return nil, nil
		}
		var changed bool
		row, changed, err = appendNodeVersion(ctx, q, seq, current, ctx.AttributionUserID(), func(spec *nodeEventSpec) {
			spec.Status = target
		})
		if err != nil || !changed {
			return nil, err
		}
		return &state.Update{NodeEvents: []*apigen.NodeEvent{&row.Event}}, nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}

func EvictNode(ctx apigen.Context, store *state.Service, identifier string, expectedVersion int32, force bool) (*apigen.NodeEvent, error) {
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return nil, err
		}
		if !isMemberStatus(current.Event.Value.Status) {
			return nil, ErrNodeNotMember
		}
		if isPrimaryRow(current) {
			return nil, ErrNodeIsPrimary
		}
		if current.Event.Version != expectedVersion {
			return nil, ErrNodeVersionChanged
		}
		nodeID := current.Event.NodeID
		update := &state.Update{}
		active, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return nil, err
		}
		pinned := 0
		for _, cfg := range active {
			if cfg.Value.PlacementNodeID() != nodeID {
				continue
			}
			if !internaldeploy.IsInternalConfig(cfg) {
				pinned++
			}
		}
		if pinned > 0 && !force {
			return nil, &ErrNodeHasDeployments{Count: pinned}
		}
		for _, cfg := range active {
			if cfg.Value.PlacementNodeID() != nodeID || !internaldeploy.IsInternalConfig(cfg) {
				continue
			}
			event, err := q.WriteDeploymentDelete(ctx, int64(cfg.DeploymentID), seq)
			if err != nil {
				return nil, err
			}
			update.DeploymentEvents = append(update.DeploymentEvents, event)
		}
		instances, err := q.ListNonFinalScheduledInstances(ctx)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		for _, event := range instances {
			inst := event.Value
			if inst.NodeID != nodeID {
				continue
			}
			finalized, err := q.AppendScheduledInstanceEvent(ctx, seq, &inst, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, now)
			if err != nil {
				return nil, err
			}
			update.ScheduledInstanceEvents = append(update.ScheduledInstanceEvents, finalized)
			previous, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			tombstone := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, DeploymentID: inst.DeploymentID, UpdatedAt: previous.UpdatedAt}
			tombstone.BumpUpdatedAt()
			if err := q.InsertScheduledInstanceStatus(ctx, seq, tombstone); err != nil {
				return nil, err
			}
			update.InstanceStatuses = append(update.InstanceStatuses, tombstone)
		}
		previousStatus, err := q.GetLatestNodeStatus(ctx, nodeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		statusTombstone := &apigen.NodeStatus{NodeID: nodeID}
		if previousStatus != nil {
			statusTombstone.UpdatedAt = previousStatus.UpdatedAt
		}
		statusTombstone.BumpUpdatedAt()
		if err := q.InsertNodeStatus(ctx, seq, statusTombstone); err != nil {
			return nil, err
		}
		update.NodeStatuses = append(update.NodeStatuses, statusTombstone)
		row, _, err = appendNodeVersion(ctx, q, seq, current, ctx.AttributionUserID(), func(spec *nodeEventSpec) {
			spec.Status = apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED
			spec.EnrollmentRequestedAt = 0
		})
		if err != nil {
			return nil, err
		}
		update.NodeEvents = []*apigen.NodeEvent{&row.Event}
		return update, nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}

type Exposure struct {
	NodeID               int32
	Deployments          []*apigen.DeploymentEvent
	SecretVersionIDs     []int32
	ConfigVersionIDs     []int32
	IssuedTLSDeployments []*apigen.DeploymentEvent
	AcmeHostnames        []string
	GithubToken          bool
}

func NodeExposure(ctx context.Context, q *pq.Queries, nodeID int32) (Exposure, error) {
	out := Exposure{NodeID: nodeID}
	active, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return out, err
	}
	for _, cfg := range active {
		if cfg.Value.PlacementNodeID() == nodeID && !internaldeploy.IsInternalConfig(cfg) {
			out.Deployments = append(out.Deployments, cfg)
		}
	}
	events, err := q.ListLatestScheduledInstanceEventsForNode(ctx, nodeID)
	if err != nil {
		return out, err
	}
	secrets := map[int32]struct{}{}
	configs := map[int32]struct{}{}
	issued := map[int32]*apigen.DeploymentEvent{}
	hostnames := map[string]struct{}{}
	seen := map[[2]int32]struct{}{}
	for _, event := range events {
		key := [2]int32{event.Value.DeploymentID, event.Value.DeploymentVersion}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(event.Value.DeploymentID), Version: int64(event.Value.DeploymentVersion)})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		collectSpecExposure(cfg, secrets, configs, hostnames, &out.GithubToken)
		container := cfg.Value.Spec.Container()
		if container != nil && container.Runtime.IssuedTlsMount != nil {
			if existing, ok := issued[cfg.DeploymentID]; !ok || cfg.Version > existing.Version {
				issued[cfg.DeploymentID] = cfg
			}
		}
	}
	out.SecretVersionIDs = sortedKeys(secrets)
	out.ConfigVersionIDs = sortedKeys(configs)
	for _, cfg := range issued {
		out.IssuedTLSDeployments = append(out.IssuedTLSDeployments, cfg)
	}
	slices.SortFunc(out.IssuedTLSDeployments, func(a, b *apigen.DeploymentEvent) int { return int(a.DeploymentID - b.DeploymentID) })
	for hostname := range hostnames {
		out.AcmeHostnames = append(out.AcmeHostnames, hostname)
	}
	slices.Sort(out.AcmeHostnames)
	return out, nil
}

func collectSpecExposure(cfg *apigen.DeploymentEvent, secrets, configs map[int32]struct{}, hostnames map[string]struct{}, github *bool) {
	for _, route := range cfg.Value.Spec.Networking.Ingress {
		if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_HTTPS || route.HttpsConfig == nil {
			continue
		}
		source := route.HttpsConfig.CertSource
		if source != nil && source.Secret != nil {
			if source.Secret.SecretVersionID > 0 {
				secrets[source.Secret.SecretVersionID] = struct{}{}
			}
			continue
		}
		hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
		if hostname != "" {
			hostnames[hostname] = struct{}{}
		}
	}
	container := cfg.Value.Spec.Container()
	if container == nil {
		return
	}
	if container.Source.NixDockerBuild != nil {
		*github = true
	}
	if image := container.Source.RemoteImage; image != nil {
		if ref, err := imageref.Parse(image.Image); err == nil && strings.EqualFold(ref.Registry, "ghcr.io") {
			*github = true
		}
	}
	for _, value := range container.Runtime.EnvVars {
		if value == nil {
			continue
		}
		if value.SecretVersionID != nil && *value.SecretVersionID > 0 {
			secrets[*value.SecretVersionID] = struct{}{}
		}
		if value.ConfigVersionID != nil && *value.ConfigVersionID > 0 {
			configs[*value.ConfigVersionID] = struct{}{}
		}
	}
}

func sortedKeys(set map[int32]struct{}) []int32 {
	out := make([]int32, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
