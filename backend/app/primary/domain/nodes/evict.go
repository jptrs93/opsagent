package nodes

import (
	"cmp"
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

func MemberNodeIDByIdentifier(q *pq.Queries, identifier string) (uint64, error) {
	return q.GetNodeIDByIdentifierWithStatus(context.Background(), identifier, pq.MemberNodeStatuses)
}

func IsEvictedIdentifier(q *pq.Queries, identifier string) bool {
	_, err := q.GetNodeIDByIdentifierWithStatus(context.Background(), identifier, []int64{int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED)})
	return err == nil
}

func isPrimaryRow(row pq.CurrentNode) bool {
	return slices.Contains(row.Event.Value.Operator.Roles, NodeRolePrimary)
}

func SetNodeDraining(ctx apigen.Context, store *state.Service, identifier string, draining bool) (*pq.NodeEvent, error) {
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
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
		target := apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL
		if draining {
			target = apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_DRAINING
		} else if current.Event.Value.Status != apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_DRAINING {
			row = current
			return nil, nil
		}
		var changed bool
		row, changed = appendNodeVersion(seq, time.Now().UnixMilli(), current, ctx.AttributionUserID(), func(spec *nodeEventSpec) {
			spec.Status = target
		})
		if !changed {
			return nil, nil
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event)), nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}

// EvictNode evicts the node as long as it has no event newer than
// expectedSeq; zero skips the check.
func EvictNode(ctx apigen.Context, store *state.Service, identifier string, expectedSeq int64, force bool) (*pq.NodeEvent, error) {
	var row pq.CurrentNode
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
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
		if expectedSeq != 0 && current.Event.Seq > expectedSeq {
			return nil, ErrNodeVersionChanged
		}
		nodeID := current.Event.NodeID
		now := time.Now()
		update := &state.WriteUpdate{}
		active, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return nil, err
		}
		pinned := 0
		for _, cfg := range active {
			if cfg.Deployment.PlacementNodeID() != nodeID {
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
			if cfg.Deployment.PlacementNodeID() != nodeID || !internaldeploy.IsInternalConfig(cfg) {
				continue
			}
			record, err := q.DeploymentDeleteRecord(ctx, cfg.Deployment.ID, seq, now)
			if err != nil {
				return nil, err
			}
			pq.AppendMutations(update, pq.DeploymentMutation(record))
		}
		instances, err := q.ListNonFinalScheduledInstances(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range instances {
			inst := event.Value
			if inst.NodeID != nodeID {
				continue
			}
			finalized := pq.ScheduledInstanceTransition(seq, event, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED, now)
			pq.AppendMutations(update, pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, finalized))
			previous, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			tombstone := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, UpdatedAt: previous.UpdatedAt}
			tombstone.BumpUpdatedAt()
			pq.AppendMutations(update, pq.ScheduledInstanceStatusMutation(seq, now.UnixMilli(), tombstone))
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
		pq.AppendMutations(update, pq.NodeStatusMutation(seq, now.UnixMilli(), statusTombstone))
		keyslots, err := q.NodeSecretKeyslotDeletes(ctx, pq.EventMeta{GlobalSeq: seq, EventTime: now.UnixMilli(), Author: ctx.AttributionUserID()}, nodeID)
		if err != nil {
			return nil, err
		}
		pq.AppendMutations(update, keyslots...)
		row, _ = appendNodeVersion(seq, now.UnixMilli(), current, ctx.AttributionUserID(), func(spec *nodeEventSpec) {
			spec.Status = apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED
			spec.EnrollmentRequestedAt = 0
		})
		pq.AppendMutations(update, pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, &row.Event))
		return update, nil
	})
	if err != nil {
		return nil, err
	}
	return &row.Event, nil
}

type Exposure struct {
	NodeID               uint64
	Deployments          []*apigen.DeploymentRecord
	Secrets              []apigen.ValueRef
	Configs              []apigen.ValueRef
	IssuedTLSDeployments []*apigen.DeploymentRecord
	AcmeHostnames        []string
	GithubToken          bool
}

func NodeExposure(ctx context.Context, q *pq.Queries, nodeID uint64) (Exposure, error) {
	out := Exposure{NodeID: nodeID}
	active, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return out, err
	}
	for _, cfg := range active {
		if cfg.Deployment.PlacementNodeID() == nodeID && !internaldeploy.IsInternalConfig(cfg) {
			out.Deployments = append(out.Deployments, cfg)
		}
	}
	events, err := q.ListRetainedScheduledInstancesForNode(ctx, nodeID)
	if err != nil {
		return out, err
	}
	secrets := map[apigen.ValueRef]struct{}{}
	configs := map[apigen.ValueRef]struct{}{}
	issued := map[uint64]*apigen.DeploymentRecord{}
	hostnames := map[string]struct{}{}
	type pinnedVersion struct {
		deploymentID uint64
		version      uint32
	}
	seen := map[pinnedVersion]struct{}{}
	for _, event := range events {
		key := pinnedVersion{event.Value.Deployment.DeploymentID, event.Value.Deployment.Version}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		cfg, err := q.GetDeploymentVersion(ctx, key.deploymentID, key.version)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		collectSpecExposure(cfg, secrets, configs, hostnames, &out.GithubToken)
		container := cfg.Deployment.Spec.Container()
		if container != nil && container.Runtime.IssuedTlsMount.Present {
			if existing, ok := issued[cfg.Deployment.ID]; !ok || cfg.Meta.Version > existing.Meta.Version {
				issued[cfg.Deployment.ID] = cfg
			}
		}
	}
	out.Secrets = sortedRefs(secrets)
	out.Configs = sortedRefs(configs)
	for _, cfg := range issued {
		out.IssuedTLSDeployments = append(out.IssuedTLSDeployments, cfg)
	}
	slices.SortFunc(out.IssuedTLSDeployments, func(a, b *apigen.DeploymentRecord) int { return cmp.Compare(a.Deployment.ID, b.Deployment.ID) })
	for hostname := range hostnames {
		out.AcmeHostnames = append(out.AcmeHostnames, hostname)
	}
	slices.Sort(out.AcmeHostnames)
	return out, nil
}

func collectSpecExposure(cfg *apigen.DeploymentRecord, secrets, configs map[apigen.ValueRef]struct{}, hostnames map[string]struct{}, github *bool) {
	for i := range cfg.Deployment.Spec.Networking.Ingress {
		route := &cfg.Deployment.Spec.Networking.Ingress[i]
		https := route.Config.Value.Https
		if https == nil {
			continue
		}
		if https.CertSource.Present && https.CertSource.Value.Value.Secret != nil {
			if ref := https.CertSource.Value.Value.Secret.Secret; ref.Valid() {
				secrets[ref.Ref()] = struct{}{}
			}
			continue
		}
		hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
		if hostname != "" {
			hostnames[hostname] = struct{}{}
		}
	}
	container := cfg.Deployment.Spec.Container()
	if container == nil {
		return
	}
	if container.Source.Value.NixImageBuild != nil {
		*github = true
	}
	if image := container.Source.Value.RemoteImage; image != nil {
		if ref, err := imageref.Parse(image.Image); err == nil && strings.EqualFold(ref.Registry, "ghcr.io") {
			*github = true
		}
	}
	for _, value := range container.Runtime.EnvVars {
		if secret := value.Value.Secret; secret != nil && secret.Secret.Valid() {
			secrets[secret.Secret.Ref()] = struct{}{}
		}
		if config := value.Value.Config; config != nil && config.Config.Valid() {
			configs[config.Config.Ref()] = struct{}{}
		}
	}
}

func sortedRefs(set map[apigen.ValueRef]struct{}) []apigen.ValueRef {
	out := make([]apigen.ValueRef, 0, len(set))
	for ref := range set {
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b apigen.ValueRef) int {
		if a.Less(b) {
			return -1
		}
		if b.Less(a) {
			return 1
		}
		return 0
	})
	return out
}
