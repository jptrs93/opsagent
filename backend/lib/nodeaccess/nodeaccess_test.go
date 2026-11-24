package nodeaccess

import (
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func exactNodes(ids ...uint64) apigen.NodeSelector {
	return apigen.NodeSelector{ExactNodes: apigen.Some(apigen.NodeIdList{Values: ids})}
}

func allNodesBut(ids ...uint64) apigen.NodeSelector {
	return apigen.NodeSelector{AllNodesExcluding: apigen.Some(apigen.NodeIdList{Values: ids})}
}

func exactSpaces(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{ExactSpaces: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func allSpacesBut(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func runOnly() apigen.NodePermissionSelector {
	return apigen.NodePermissionSelector{ExactPermissions: apigen.Some(apigen.NodePermissionList{Values: []apigen.NodePermission{Run}})}
}

func policy(name string, deny bool, nodes apigen.NodeSelector, spaces apigen.AuthzSpaceSelector) apigen.NodeAccessPolicy {
	effect := apigen.NodeAccessEffectValueOneof{Allow: &apigen.NodeAccessAllow{}}
	if deny {
		effect = apigen.NodeAccessEffectValueOneof{Deny: &apigen.NodeAccessDeny{}}
	}
	return apigen.NodeAccessPolicy{Name: name, Nodes: nodes, Rule: apigen.NodeAccessRule{
		Effect:   apigen.NodeAccessEffect{Value: effect},
		Selector: apigen.NodeAccessSelector{Permissions: runOnly(), Spaces: spaces},
	}}
}

func TestDecideChecksDeniesFirstThenAllows(t *testing.T) {
	policies := []apigen.NodeAccessPolicy{
		policy("everything", false, allNodesBut(), allSpacesBut()),
		policy("node 7 is prod only", true, exactNodes(7), allSpacesBut(3)),
	}
	for _, tc := range []struct {
		node, space uint64
		allowed     bool
		policy      string
	}{
		{node: 7, space: 3, allowed: true},
		{node: 7, space: 2, policy: "node 7 is prod only"},
		{node: 8, space: 2, allowed: true},
	} {
		got := Decide(policies, tc.node, Run, tc.space)
		if got.Allowed != tc.allowed || got.Policy != tc.policy {
			t.Errorf("node %d space %d = %+v, want allowed %v by %q", tc.node, tc.space, got, tc.allowed, tc.policy)
		}
	}
}

func TestDecideRefusesWhatNoAllowCovers(t *testing.T) {
	policies := []apigen.NodeAccessPolicy{policy("node 7 runs space 2", false, exactNodes(7), exactSpaces(2))}
	if got := Decide(policies, 7, Run, 2); !got.Allowed {
		t.Fatalf("the covered pair = %+v, want allowed", got)
	}
	for _, pair := range [][2]uint64{{7, 3}, {8, 2}} {
		if got := Decide(policies, pair[0], Run, pair[1]); got.Allowed || got.Policy != "" {
			t.Errorf("node %d space %d = %+v, want refused with no policy named", pair[0], pair[1], got)
		}
	}
	if got := Decide(nil, 7, Run, 2); got.Allowed {
		t.Fatalf("no policies = %+v, want refused", got)
	}
}

func TestDecideIgnoresDisabledPolicies(t *testing.T) {
	deny := policy("deny", true, allNodesBut(), allSpacesBut())
	deny.Disabled = true
	allow := policy("allow", false, allNodesBut(), allSpacesBut())
	if got := Decide([]apigen.NodeAccessPolicy{deny, allow}, 1, Run, 1); !got.Allowed {
		t.Fatalf("a disabled deny refused: %+v", got)
	}
	allow.Disabled = true
	if got := Decide([]apigen.NodeAccessPolicy{allow}, 1, Run, 1); got.Allowed {
		t.Fatalf("a disabled allow allowed: %+v", got)
	}
}

func TestDecideAlwaysAllowsTheSystemSpace(t *testing.T) {
	denyAll := policy("deny", true, allNodesBut(), allSpacesBut())
	if got := Decide([]apigen.NodeAccessPolicy{denyAll}, 1, Run, 0); !got.Allowed {
		t.Fatalf("a deny reached the system space: %+v", got)
	}
	if got := Decide(nil, 1, Run, 0); !got.Allowed {
		t.Fatalf("the system space needs no policy: %+v", got)
	}
}

func TestDecideMatchesThePermissionPosition(t *testing.T) {
	other := policy("other", false, allNodesBut(), allSpacesBut())
	other.Rule.Selector.Permissions = apigen.NodePermissionSelector{AllPermissionsExcluding: apigen.Some(apigen.NodePermissionList{Values: []apigen.NodePermission{Run}})}
	if got := Decide([]apigen.NodeAccessPolicy{other}, 1, Run, 1); got.Allowed {
		t.Fatalf("a rule excluding run allowed run: %+v", got)
	}
	other.Rule.Selector.Permissions = apigen.NodePermissionSelector{AllPermissionsExcluding: apigen.Some(apigen.NodePermissionList{})}
	if got := Decide([]apigen.NodeAccessPolicy{other}, 1, Run, 1); !got.Allowed {
		t.Fatalf("the permission wildcard did not cover run: %+v", got)
	}
}

func TestRunSpaces(t *testing.T) {
	policies := []apigen.NodeAccessPolicy{
		policy("everything", false, allNodesBut(), allSpacesBut()),
		policy("node 7 is prod only", true, exactNodes(7), allSpacesBut(3)),
	}
	if got := RunSpaces(policies, 7, []uint64{0, 1, 2, 3}); !slices.Equal(got, []uint64{0, 3}) {
		t.Fatalf("node 7 runs %v, want [0 3]", got)
	}
	if got := RunSpaces(policies, 8, []uint64{0, 1, 2, 3}); !slices.Equal(got, []uint64{0, 1, 2, 3}) {
		t.Fatalf("node 8 runs %v, want every space", got)
	}
}

func TestNormalizeSortsAndDeduplicates(t *testing.T) {
	p := policy("  dedicated  ", true, exactNodes(9, 7, 9), allSpacesBut(3, 1, 3))
	if err := Normalize(&p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "dedicated" {
		t.Fatalf("name = %q", p.Name)
	}
	if got := p.Nodes.ExactNodes.Value.Values; !slices.Equal(got, []uint64{7, 9}) {
		t.Fatalf("nodes = %v", got)
	}
	if got := p.Rule.Selector.Spaces.AllSpacesExcluding.Value.Values; !slices.Equal(got, []uint64{1, 3}) {
		t.Fatalf("spaces = %v", got)
	}
}

func TestNormalizeRefusesMalformedPolicies(t *testing.T) {
	valid := func() apigen.NodeAccessPolicy { return policy("p", false, allNodesBut(), allSpacesBut()) }
	for name, tc := range map[string]struct {
		mutate func(*apigen.NodeAccessPolicy)
		want   string
	}{
		"no name":   {func(p *apigen.NodeAccessPolicy) { p.Name = " " }, "name"},
		"long name": {func(p *apigen.NodeAccessPolicy) { p.Name = strings.Repeat("x", 65) }, "name"},
		"no effect": {func(p *apigen.NodeAccessPolicy) { p.Rule.Effect = apigen.NodeAccessEffect{} }, "effect"},
		"no nodes":  {func(p *apigen.NodeAccessPolicy) { p.Nodes = apigen.NodeSelector{} }, "nodes"},
		"both node lists": {func(p *apigen.NodeAccessPolicy) {
			p.Nodes.ExactNodes = apigen.Some(apigen.NodeIdList{Values: []uint64{1}})
		}, "nodes"},
		"empty exact nodes": {func(p *apigen.NodeAccessPolicy) { p.Nodes = exactNodes() }, "nodes"},
		"no permissions":    {func(p *apigen.NodeAccessPolicy) { p.Rule.Selector.Permissions = apigen.NodePermissionSelector{} }, "permissions"},
		"unknown permission": {func(p *apigen.NodeAccessPolicy) {
			p.Rule.Selector.Permissions = apigen.NodePermissionSelector{ExactPermissions: apigen.Some(apigen.NodePermissionList{Values: []apigen.NodePermission{9}})}
		}, "permissions"},
		"no spaces":          {func(p *apigen.NodeAccessPolicy) { p.Rule.Selector.Spaces = apigen.AuthzSpaceSelector{} }, "spaces"},
		"empty exact spaces": {func(p *apigen.NodeAccessPolicy) { p.Rule.Selector.Spaces = exactSpaces() }, "spaces"},
	} {
		p := valid()
		tc.mutate(&p)
		if err := Normalize(&p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
	}
}

func TestBuiltinsAreValidAndDistinct(t *testing.T) {
	builtins := Builtins()
	if len(builtins) != 2 {
		t.Fatalf("%d builtins, want 2", len(builtins))
	}
	for _, b := range builtins {
		normalized := b
		if err := Normalize(&normalized); err != nil {
			t.Fatalf("builtin %q: %v", b.Name, err)
		}
		if !SameDefinition(b, normalized) {
			t.Fatalf("builtin %q is not in canonical form", b.Name)
		}
		if b.Builtin == apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_NONE {
			t.Fatalf("builtin %q is not marked", b.Name)
		}
	}
	system, everything := builtins[0], builtins[1]
	if got := Decide([]apigen.NodeAccessPolicy{everything}, 4, Run, 9); !got.Allowed {
		t.Fatalf("the default does not allow a user space: %+v", got)
	}
	disabled := everything
	disabled.Disabled = true
	if !SameDefinition(everything, disabled) {
		t.Fatal("enabling a policy changed its definition")
	}
	if SameDefinition(system, everything) {
		t.Fatal("the two builtins have one definition")
	}
}
