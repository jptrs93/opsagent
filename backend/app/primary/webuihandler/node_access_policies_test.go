package webuihandler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodeaccesspolicies"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/nodeaccess"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newNodeAccessHandler(t *testing.T) (*Handler, *apigen.Space, *nodes.Node) {
	t.Helper()
	h, staging := newEnforcementTestHandler(t)
	return h, staging, ensureTestNode(h.Store, "primary", "primary")
}

func exactNodes(ids ...uint64) apigen.NodeSelector {
	return apigen.NodeSelector{ExactNodes: apigen.Some(apigen.NodeIdList{Values: ids})}
}

func allNodes() apigen.NodeSelector {
	return apigen.NodeSelector{AllNodesExcluding: apigen.Some(apigen.NodeIdList{Values: []uint64{}})}
}

func nodeAccessRule(deny bool, spaces apigen.AuthzSpaceSelector) apigen.NodeAccessRule {
	effect := apigen.NodeAccessEffectValueOneof{Allow: &apigen.NodeAccessAllow{}}
	if deny {
		effect = apigen.NodeAccessEffectValueOneof{Deny: &apigen.NodeAccessDeny{}}
	}
	return apigen.NodeAccessRule{Effect: apigen.NodeAccessEffect{Value: effect}, Selector: apigen.NodeAccessSelector{
		Permissions: apigen.NodePermissionSelector{ExactPermissions: apigen.Some(apigen.NodePermissionList{Values: []apigen.NodePermission{nodeaccess.Run}})},
		Spaces:      spaces,
	}}
}

func nodeAccessPolicyOf(u *apigen.CoreWriteUpdate) *apigen.NodeAccessPolicy {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY)
	if m == nil {
		return nil
	}
	return m.Entity().Value.NodeAccessPolicy
}

func createNodeAccessPolicy(t *testing.T, h *Handler, name string, nodes apigen.NodeSelector, rule apigen.NodeAccessRule) *apigen.NodeAccessPolicy {
	t.Helper()
	u, err := h.PostV1NodeAccessPoliciesCreate(enforceCtx(1, false), &apigen.NodeAccessPolicyCreateRequest{Name: name, Nodes: nodes, Rule: rule})
	if err != nil {
		t.Fatalf("creating node access policy %q: %v", name, err)
	}
	return nodeAccessPolicyOf(u)
}

func builtinNodeAccessPolicy(t *testing.T, h *Handler, builtin apigen.NodeAccessBuiltin) *apigen.NodeAccessPolicy {
	t.Helper()
	policies, err := h.Queries.NodeAccessPolicyValues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range policies {
		if policies[i].Builtin == builtin {
			return &policies[i]
		}
	}
	t.Fatalf("built-in node access policy %v is missing", builtin)
	return nil
}

func setNodeAccessPolicyDisabled(h *Handler, p *apigen.NodeAccessPolicy, disabled bool) error {
	_, err := h.PostV1NodeAccessPoliciesUpdate(enforceCtx(1, false), &apigen.NodeAccessPolicyUpdateRequest{
		ID: p.ID, Name: p.Name, Disabled: disabled, Nodes: p.Nodes, Rule: p.Rule,
	})
	return err
}

func placeRequest(spaceID, nodeID uint64, name string) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		SpaceID: spaceID, Name: name,
		Scheduling: apigen.DedicatedScheduling(false, nodeID),
		Spec:       remoteDeploymentSpec("nginx", hostNetworking()),
	}
}

func TestAFreshClusterHoldsTheBuiltinNodeAccessPolicies(t *testing.T) {
	h, _, _ := newNodeAccessHandler(t)
	system := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM)
	everything := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)
	if system.Name != nodeaccess.SystemName || system.Disabled || everything.Name != nodeaccess.DefaultName || everything.Disabled {
		t.Fatalf("builtins = %+v and %+v, want both enabled under their names", system, everything)
	}
	seq, err := h.Queries.GetGlobalSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeaccesspolicies.EnsureBuiltins(h.Store); err != nil {
		t.Fatal(err)
	}
	if after, _ := h.Queries.GetGlobalSeq(t.Context()); after != seq {
		t.Fatalf("ensuring builtins that are in place wrote seq %d after %d", after, seq)
	}
}

func TestEnsureBuiltinsCreatesAndRestoresThem(t *testing.T) {
	h, _, _ := newNodeAccessHandler(t)
	ctx := t.Context()
	rows, err := h.Queries.ListNodeAccessPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var deletes []pq.Mutation
		for _, row := range rows {
			deletes = append(deletes, pq.DeleteMutation(pq.EventMeta{GlobalSeq: seq, EventTime: 1}, apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY, row.NodeAccessPolicyID))
		}
		return pq.NewUpdate(deletes...), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deploymentsCreate(enforceCtx(1, false), placeRequest(nodes.DefaultSpaceID, 1, "web")); err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("with no policies err = %v, want node_space_not_allowed", err)
	}

	if err := nodeaccesspolicies.EnsureBuiltins(h.Store); err != nil {
		t.Fatal(err)
	}
	everything := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)
	builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM)

	if err := h.Store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		drifted := *everything
		drifted.Name, drifted.Disabled = "renamed", true
		return pq.NewUpdate(pq.NodeAccessPolicyMutation(pq.EventMeta{GlobalSeq: seq, EventTime: 2, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}, drifted.ID, drifted)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := nodeaccesspolicies.EnsureBuiltins(h.Store); err != nil {
		t.Fatal(err)
	}
	restored := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)
	if restored.ID != everything.ID || restored.Name != nodeaccess.DefaultName || !restored.Disabled {
		t.Fatalf("restored = %+v, want the definition back under id %d and still disabled", restored, everything.ID)
	}
}

func TestBuiltinNodeAccessPoliciesKeepTheirDefinition(t *testing.T) {
	h, staging, _ := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)
	system := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM)
	everything := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)

	if err := setNodeAccessPolicyDisabled(h, system, true); !errors.Is(err, nodeaccesspolicies.AlwaysEnabledErr) {
		t.Fatalf("disabling the system policy: %v, want AlwaysEnabledErr", err)
	}
	for _, p := range []*apigen.NodeAccessPolicy{system, everything} {
		if err := h.PostV1NodeAccessPoliciesDelete(admin, &apigen.NodeAccessPolicyDeleteRequest{ID: p.ID}); !errors.Is(err, nodeaccesspolicies.BuiltinErr) {
			t.Fatalf("deleting %q: %v, want BuiltinErr", p.Name, err)
		}
		if _, err := h.PostV1NodeAccessPoliciesUpdate(admin, &apigen.NodeAccessPolicyUpdateRequest{ID: p.ID, Name: "mine", Nodes: p.Nodes, Rule: p.Rule}); !errors.Is(err, nodeaccesspolicies.BuiltinErr) {
			t.Fatalf("renaming %q: %v, want BuiltinErr", p.Name, err)
		}
		if _, err := h.PostV1NodeAccessPoliciesUpdate(admin, &apigen.NodeAccessPolicyUpdateRequest{ID: p.ID, Name: p.Name, Nodes: p.Nodes, Rule: nodeAccessRule(false, exactSpaces(staging.ID))}); !errors.Is(err, nodeaccesspolicies.BuiltinErr) {
			t.Fatalf("rewriting %q: %v, want BuiltinErr", p.Name, err)
		}
	}

	if err := setNodeAccessPolicyDisabled(h, everything, true); err != nil {
		t.Fatalf("disabling the default with nothing placed: %v", err)
	}
	off := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)
	if !off.Disabled || off.Name != nodeaccess.DefaultName {
		t.Fatalf("after disabling = %+v", off)
	}
	if err := setNodeAccessPolicyDisabled(h, off, false); err != nil {
		t.Fatalf("enabling the default again: %v", err)
	}
}

func TestDeploymentCannotBePlacedOnANodeDeniedItsSpace(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)

	if _, err := h.deploymentsCreate(admin, placeRequest(staging.ID, node.ID, "web")); err != nil {
		t.Fatalf("placing under the default: %v", err)
	}
	createNodeAccessPolicy(t, h, "global only", exactNodes(node.ID), nodeAccessRule(true, allSpacesBut(nodes.DefaultSpaceID, staging.ID)))

	later, err := nodes.CreateSpace(h.Store, "later", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.deploymentsCreate(admin, placeRequest(later.ID, node.ID, "web2"))
	if err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("err = %v, want node_space_not_allowed", err)
	}
	if msg := displayOf(err); !strings.Contains(msg, `"primary"`) || !strings.Contains(msg, `"later"`) || !strings.Contains(msg, `"global only"`) {
		t.Fatalf("refusal = %q, want the node, the space and the policy named", msg)
	}
	if _, err := h.deploymentsCreate(admin, placeRequest(nodes.DefaultSpaceID, node.ID, "web3")); err != nil {
		t.Fatalf("placing a space the deny leaves out: %v", err)
	}
}

func TestDeploymentCannotMoveIntoASpaceItsNodeMayNotRun(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)
	cfg, err := h.deploymentsCreate(admin, placeRequest(nodes.DefaultSpaceID, node.ID, "web"))
	if err != nil {
		t.Fatal(err)
	}
	createNodeAccessPolicy(t, h, "no staging", allNodes(), nodeAccessRule(true, exactSpaces(staging.ID)))

	_, err = h.deploymentsUpdate(admin, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID,
		ExpectedSeq:  cfg.Meta.UpdatedSeq,
		Update:       apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}},
	})
	if err == nil || !strings.Contains(err.Error(), "node_space_not_allowed") {
		t.Fatalf("err = %v, want node_space_not_allowed", err)
	}
}

func TestPlacementWithNoAllowIsRefused(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)
	if err := setNodeAccessPolicyDisabled(h, builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT), true); err != nil {
		t.Fatal(err)
	}
	createNodeAccessPolicy(t, h, "staging here", exactNodes(node.ID), nodeAccessRule(false, exactSpaces(staging.ID)))

	if _, err := h.deploymentsCreate(admin, placeRequest(staging.ID, node.ID, "web")); err != nil {
		t.Fatalf("placing the allowed space: %v", err)
	}
	_, err := h.deploymentsCreate(admin, placeRequest(nodes.DefaultSpaceID, node.ID, "web2"))
	if msg := displayOf(err); err == nil || !strings.Contains(msg, "no node access policy allows it") {
		t.Fatalf("err = %v (%q), want a refusal for want of an allow", err, msg)
	}
}

func TestNodeAccessPolicyWriteRefusedWhileItStrandsADeployment(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)
	if _, err := h.deploymentsCreate(admin, placeRequest(staging.ID, node.ID, "web")); err != nil {
		t.Fatal(err)
	}
	stranded := func(err error) {
		t.Helper()
		if !errors.Is(err, nodeaccesspolicies.PlacementsInUseErr) {
			t.Fatalf("err = %v, want PlacementsInUseErr", err)
		}
		if msg := displayOf(err); !strings.Contains(msg, `"web"`) || !strings.Contains(msg, `"staging"`) || !strings.Contains(msg, `"primary"`) {
			t.Fatalf("refusal = %q, want the deployment, its space and its node named", msg)
		}
	}

	_, err := h.PostV1NodeAccessPoliciesCreate(admin, &apigen.NodeAccessPolicyCreateRequest{Name: "no staging", Nodes: allNodes(), Rule: nodeAccessRule(true, exactSpaces(staging.ID))})
	stranded(err)
	everything := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)
	stranded(setNodeAccessPolicyDisabled(h, everything, true))

	keep := createNodeAccessPolicy(t, h, "staging here", exactNodes(node.ID), nodeAccessRule(false, exactSpaces(staging.ID)))
	if err := setNodeAccessPolicyDisabled(h, everything, true); err != nil {
		t.Fatalf("disabling the default once another allow covers the placement: %v", err)
	}
	stranded(h.PostV1NodeAccessPoliciesDelete(admin, &apigen.NodeAccessPolicyDeleteRequest{ID: keep.ID}))
	stranded(setNodeAccessPolicyDisabled(h, keep, true))
	_, err = h.PostV1NodeAccessPoliciesUpdate(admin, &apigen.NodeAccessPolicyUpdateRequest{ID: keep.ID, Name: keep.Name, Nodes: keep.Nodes, Rule: nodeAccessRule(false, exactSpaces(nodes.DefaultSpaceID))})
	stranded(err)

	if _, err := h.PostV1NodeAccessPoliciesCreate(admin, &apigen.NodeAccessPolicyCreateRequest{Name: "no staging", Disabled: true, Nodes: allNodes(), Rule: nodeAccessRule(true, exactSpaces(staging.ID))}); err != nil {
		t.Fatalf("creating a disabled deny: %v", err)
	}
}

func TestNodeAccessPolicyWriteValidation(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin := enforceCtx(1, false)
	create := func(name string, nodes apigen.NodeSelector, rule apigen.NodeAccessRule) error {
		_, err := h.PostV1NodeAccessPoliciesCreate(admin, &apigen.NodeAccessPolicyCreateRequest{Name: name, Nodes: nodes, Rule: rule})
		return err
	}
	if err := create("", allNodes(), nodeAccessRule(true, exactSpaces(staging.ID))); !errors.Is(err, nodeaccesspolicies.InvalidErr) {
		t.Fatalf("no name: %v, want InvalidErr", err)
	}
	if err := create("p", apigen.NodeSelector{}, nodeAccessRule(true, exactSpaces(staging.ID))); !errors.Is(err, nodeaccesspolicies.InvalidErr) {
		t.Fatalf("no nodes: %v, want InvalidErr", err)
	}
	if err := create("p", allNodes(), nodeAccessRule(true, exactSpaces(999))); !errors.Is(err, nodeaccesspolicies.SpaceNotFoundErr) {
		t.Fatalf("unknown space: %v, want SpaceNotFoundErr", err)
	}
	if err := create("p", exactNodes(999), nodeAccessRule(true, exactSpaces(staging.ID))); !errors.Is(err, nodeaccesspolicies.NodeNotFoundErr) {
		t.Fatalf("unknown node: %v, want NodeNotFoundErr", err)
	}
	if err := create(nodeaccess.DefaultName, allNodes(), nodeAccessRule(true, exactSpaces(staging.ID))); !errors.Is(err, nodeaccesspolicies.DuplicateNameErr) {
		t.Fatalf("a builtin's name: %v, want DuplicateNameErr", err)
	}
	created := createNodeAccessPolicy(t, h, "p", exactNodes(node.ID, node.ID), nodeAccessRule(true, exactSpaces(staging.ID)))
	if created.Builtin != apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_NONE || !slices.Equal(created.Nodes.ExactNodes.Value.Values, []uint64{node.ID}) {
		t.Fatalf("created = %+v, want a plain policy with its node listed once", created)
	}
	if _, err := h.PostV1NodeAccessPoliciesUpdate(admin, &apigen.NodeAccessPolicyUpdateRequest{ID: created.ID, ExpectedSeq: 1, Name: "p", Nodes: created.Nodes, Rule: created.Rule}); !errors.Is(err, nodeaccesspolicies.VersionConflictErr) {
		t.Fatalf("stale expected seq: %v, want VersionConflictErr", err)
	}
	if err := h.PostV1NodeAccessPoliciesDelete(admin, &apigen.NodeAccessPolicyDeleteRequest{ID: created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := h.PostV1NodeAccessPoliciesDelete(admin, &apigen.NodeAccessPolicyDeleteRequest{ID: created.ID}); !errors.Is(err, nodeaccesspolicies.NotFoundErr) {
		t.Fatalf("second delete: %v, want NotFoundErr", err)
	}
}

func TestNodeAccessPoliciesNeedTheAccessPermission(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	spaceop := enforceCtx(2, false)
	everything := builtinNodeAccessPolicy(t, h, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT)

	if _, err := h.PostV1NodeAccessPoliciesCreate(spaceop, &apigen.NodeAccessPolicyCreateRequest{Name: "p", Nodes: exactNodes(node.ID), Rule: nodeAccessRule(true, exactSpaces(staging.ID))}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("create without access: %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1NodeAccessPoliciesUpdate(spaceop, &apigen.NodeAccessPolicyUpdateRequest{ID: everything.ID, Name: everything.Name, Disabled: true, Nodes: everything.Nodes, Rule: everything.Rule}); !errors.Is(err, nodeaccesspolicies.NotFoundErr) {
		t.Fatalf("update without access: %v, want NotFoundErr", err)
	}
	if err := h.PostV1NodeAccessPoliciesDelete(spaceop, &apigen.NodeAccessPolicyDeleteRequest{ID: everything.ID}); !errors.Is(err, nodeaccesspolicies.NotFoundErr) {
		t.Fatalf("delete without access: %v, want NotFoundErr", err)
	}
	if got := visibleOpening(t, h, spaceop)[apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY]; len(got) != 0 {
		t.Fatalf("a space operator's stream carries %d node access policies, want none", len(got))
	}
	if got := visibleOpening(t, h, enforceCtx(1, false))[apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY]; len(got) != 2 {
		t.Fatalf("the admin's stream carries %d node access policies, want the 2 builtins", len(got))
	}
}

func TestSpaceDeleteRefusedWhileANodeAccessPolicyNamesIt(t *testing.T) {
	h, staging, _ := newNodeAccessHandler(t)
	policy := createNodeAccessPolicy(t, h, "no staging", allNodes(), nodeAccessRule(true, exactSpaces(staging.ID)))
	err := nodes.DeleteSpace(h.Store, staging.ID, 0)
	var inUse *nodes.SpaceInUseError
	if !errors.As(err, &inUse) || !strings.Contains(err.Error(), "1 node access policy") {
		t.Fatalf("err = %v, want the space in use by 1 node access policy", err)
	}
	if err := h.PostV1NodeAccessPoliciesDelete(enforceCtx(1, false), &apigen.NodeAccessPolicyDeleteRequest{ID: policy.ID}); err != nil {
		t.Fatal(err)
	}
	if err := nodes.DeleteSpace(h.Store, staging.ID, 0); err != nil {
		t.Fatalf("deleting the space once no policy names it: %v", err)
	}
}

func runSpacesByNode(list apigen.NodeAccessList) map[uint64][]uint64 {
	out := map[uint64][]uint64{}
	for _, item := range list.Items {
		out[item.NodeID] = item.RunDeploymentSpaces
	}
	return out
}

func TestNodeAccessSidecarListsTheSpacesEachVisibleNodeMayRun(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)
	other := ensureTestNode(h.Store, "secondary", "secondary-id")

	got := runSpacesByNode(h.nodeAccessList(admin))
	want := []uint64{nodes.DefaultSpaceID, staging.ID}
	if !slices.Equal(got[node.ID], want) || !slices.Equal(got[other.ID], want) {
		t.Fatalf("admin sees %v, want every user space on both nodes", got)
	}
	if got := runSpacesByNode(h.nodeAccessList(spaceop)); !slices.Equal(got[node.ID], []uint64{nodes.DefaultSpaceID}) || !slices.Equal(got[other.ID], []uint64{nodes.DefaultSpaceID}) {
		t.Fatalf("the space operator sees %v, want only its own space on both nodes", got)
	}

	createNodeAccessPolicy(t, h, "staging only", exactNodes(other.ID), nodeAccessRule(true, allSpacesBut(staging.ID)))
	got = runSpacesByNode(h.nodeAccessList(admin))
	if !slices.Equal(got[node.ID], want) || !slices.Equal(got[other.ID], []uint64{staging.ID}) {
		t.Fatalf("after the deny the admin sees %v", got)
	}
	got = runSpacesByNode(h.nodeAccessList(spaceop))
	if _, listed := got[other.ID]; listed || len(got) != 1 {
		t.Fatalf("after the deny the space operator sees %v, want the other node gone", got)
	}
	if got := h.nodeAccessList(apigen.Context{Ctx: context.Background(), SessionKind: apigen.UserSessionKind_USER_SESSION_KIND_ROOT}); len(got.Items) != 0 {
		t.Fatalf("a root session sees %v, want nothing", got.Items)
	}
}

func TestStreamResendsNodeAccessWhenAPolicyChangesIt(t *testing.T) {
	h, staging, node := newNodeAccessHandler(t)
	msgs := startTestEventStream(t, h, 1)

	createNodeAccessPolicy(t, h, "no staging", allNodes(), nodeAccessRule(true, exactSpaces(staging.ID)))
	next := recvMsg(t, msgs)
	if !next.Snapshot.Present || len(foldOpening(next)[apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY]) != 3 {
		t.Fatalf("after the deny = %+v, want a fresh snapshot with the new policy", next)
	}
	if !next.NodeAccess.Present || !slices.Equal(runSpacesByNode(next.NodeAccess.Value)[node.ID], []uint64{nodes.DefaultSpaceID}) {
		t.Fatalf("the snapshot's sidecar = %+v, want staging gone from the node", next.NodeAccess)
	}

	added := ensureTestNode(h.Store, "secondary", "secondary-id")
	next = recvMsg(t, msgs)
	if len(mutationsOf(next, apigen.CoreEntityType_CORE_ENTITY_NODE)) != 1 {
		t.Fatalf("after a node joined = %+v, want its create", next)
	}
	if !next.NodeAccess.Present || !slices.Equal(runSpacesByNode(next.NodeAccess.Value)[added.ID], []uint64{nodes.DefaultSpaceID}) {
		t.Fatalf("the create's sidecar = %+v, want the new node listed", next.NodeAccess)
	}

	if _, err := nodes.CreateSpace(h.Store, "scratch", 0); err != nil {
		t.Fatal(err)
	}
	if next = recvMsg(t, msgs); !next.Snapshot.Present || !next.NodeAccess.Present {
		t.Fatalf("after a space was created = %+v, want a snapshot with the sidecar listing it", next)
	}
	if _, err := h.nodesRename(enforceCtx(1, false), &apigen.NodeRenameRequest{Identifier: "secondary-id", Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if next = recvMsg(t, msgs); len(next.Events) != 1 || next.NodeAccess.Present {
		t.Fatalf("after a rename = %+v, want the event without a sidecar", next)
	}
}
