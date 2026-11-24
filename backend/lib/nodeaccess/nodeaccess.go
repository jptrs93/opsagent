package nodeaccess

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

const (
	maxListValues = 256
	maxNameLength = 64

	Run = apigen.NodePermission_NODE_PERMISSION_RUN_DEPLOYMENTS

	SystemName  = "system_everywhere"
	DefaultName = "all_spaces_everywhere"
)

type Decision struct {
	Allowed bool
	Policy  string
}

func Decide(policies []apigen.NodeAccessPolicy, nodeID uint64, permission apigen.NodePermission, spaceID uint64) Decision {
	// Before any policy is read, so no deny can stop a node running OpenDeploy's own deployments.
	if permission == Run && spaceID == internaldeploy.SpaceID {
		return Decision{Allowed: true}
	}
	allowed := false
	for i := range policies {
		p := &policies[i]
		if p.Disabled || !matches(p, nodeID, permission, spaceID) {
			continue
		}
		if p.Rule.Effect.Value.Deny != nil {
			return Decision{Policy: p.Name}
		}
		allowed = true
	}
	return Decision{Allowed: allowed}
}

func Refusal(node, space string, d Decision) string {
	if d.Policy != "" {
		return fmt.Sprintf("Node %q may not run deployments of space %q: denied by node access policy %q", node, space, d.Policy)
	}
	return fmt.Sprintf("Node %q may not run deployments of space %q: no node access policy allows it", node, space)
}

func RunSpaces(policies []apigen.NodeAccessPolicy, nodeID uint64, spaceIDs []uint64) []uint64 {
	out := make([]uint64, 0, len(spaceIDs))
	for _, spaceID := range spaceIDs {
		if Decide(policies, nodeID, Run, spaceID).Allowed {
			out = append(out, spaceID)
		}
	}
	return out
}

func matches(p *apigen.NodeAccessPolicy, nodeID uint64, permission apigen.NodePermission, spaceID uint64) bool {
	sel := p.Rule.Selector
	return nodeMatches(p.Nodes, nodeID) && permissionMatches(sel.Permissions, permission) && spaceMatches(sel.Spaces, spaceID)
}

func nodeMatches(sel apigen.NodeSelector, id uint64) bool {
	if sel.ExactNodes.Present {
		return slices.Contains(sel.ExactNodes.Value.Values, id)
	}
	if sel.AllNodesExcluding.Present {
		return !slices.Contains(sel.AllNodesExcluding.Value.Values, id)
	}
	return false
}

func permissionMatches(sel apigen.NodePermissionSelector, permission apigen.NodePermission) bool {
	if sel.ExactPermissions.Present {
		return slices.Contains(sel.ExactPermissions.Value.Values, permission)
	}
	if sel.AllPermissionsExcluding.Present {
		return !slices.Contains(sel.AllPermissionsExcluding.Value.Values, permission)
	}
	return false
}

func spaceMatches(sel apigen.AuthzSpaceSelector, id uint64) bool {
	if sel.ExactSpaces.Present {
		return slices.Contains(sel.ExactSpaces.Value.Values, id)
	}
	if sel.AllSpacesExcluding.Present {
		return !slices.Contains(sel.AllSpacesExcluding.Value.Values, id)
	}
	return false
}

func canonicalPosition[T cmp.Ordered](position string, exactPresent, excludingPresent bool, exact, excluding *[]T) error {
	if exactPresent == excludingPresent {
		return fmt.Errorf("%s: exactly one of the exact list or the all-excluding list must be set", position)
	}
	list := excluding
	if exactPresent {
		list = exact
		if len(*exact) == 0 {
			return fmt.Errorf("%s: the exact list must name at least one value", position)
		}
	}
	if len(*list) > maxListValues {
		return fmt.Errorf("%s lists at most %d values", position, maxListValues)
	}
	values := slices.Clone(*list)
	slices.Sort(values)
	*list = slices.Compact(values)
	if *list == nil {
		*list = []T{}
	}
	return nil
}

func Normalize(policy *apigen.NodeAccessPolicy) error {
	policy.Name = strings.TrimSpace(policy.Name)
	if policy.Name == "" || len(policy.Name) > maxNameLength {
		return fmt.Errorf("name must be 1 to %d characters", maxNameLength)
	}
	if err := policy.Rule.Effect.Value.Validate(); err != nil {
		return fmt.Errorf("rule.effect: exactly one of allow or deny must be set")
	}
	nodes := &policy.Nodes
	if err := canonicalPosition("nodes", nodes.ExactNodes.Present, nodes.AllNodesExcluding.Present,
		&nodes.ExactNodes.Value.Values, &nodes.AllNodesExcluding.Value.Values); err != nil {
		return err
	}
	permissions := &policy.Rule.Selector.Permissions
	if err := canonicalPosition("rule.selector.permissions", permissions.ExactPermissions.Present, permissions.AllPermissionsExcluding.Present,
		&permissions.ExactPermissions.Value.Values, &permissions.AllPermissionsExcluding.Value.Values); err != nil {
		return err
	}
	for _, v := range append(slices.Clone(permissions.ExactPermissions.Value.Values), permissions.AllPermissionsExcluding.Value.Values...) {
		if v != Run {
			return fmt.Errorf("rule.selector.permissions: %d is not a node permission", int32(v))
		}
	}
	spaces := &policy.Rule.Selector.Spaces
	return canonicalPosition("rule.selector.spaces", spaces.ExactSpaces.Present, spaces.AllSpacesExcluding.Present,
		&spaces.ExactSpaces.Value.Values, &spaces.AllSpacesExcluding.Value.Values)
}

func NamedSpaces(policy *apigen.NodeAccessPolicy) []uint64 {
	spaces := policy.Rule.Selector.Spaces
	return append(slices.Clone(spaces.ExactSpaces.Value.Values), spaces.AllSpacesExcluding.Value.Values...)
}

func NamedNodes(policy *apigen.NodeAccessPolicy) []uint64 {
	return append(slices.Clone(policy.Nodes.ExactNodes.Value.Values), policy.Nodes.AllNodesExcluding.Value.Values...)
}

func SameDefinition(a, b apigen.NodeAccessPolicy) bool {
	a.ID, b.ID = 0, 0
	a.Disabled, b.Disabled = false, false
	return slices.Equal(a.Encode(), b.Encode())
}

func allowRunOnEveryNode(name string, builtin apigen.NodeAccessBuiltin, spaces apigen.AuthzSpaceSelector) apigen.NodeAccessPolicy {
	return apigen.NodeAccessPolicy{
		Name:    name,
		Builtin: builtin,
		Nodes:   apigen.NodeSelector{AllNodesExcluding: apigen.Some(apigen.NodeIdList{Values: []uint64{}})},
		Rule: apigen.NodeAccessRule{
			Effect: apigen.NodeAccessEffect{Value: apigen.NodeAccessEffectValueOneof{Allow: &apigen.NodeAccessAllow{}}},
			Selector: apigen.NodeAccessSelector{
				Permissions: apigen.NodePermissionSelector{ExactPermissions: apigen.Some(apigen.NodePermissionList{Values: []apigen.NodePermission{Run}})},
				Spaces:      spaces,
			},
		},
	}
}

func Builtins() []apigen.NodeAccessPolicy {
	return []apigen.NodeAccessPolicy{
		allowRunOnEveryNode(SystemName, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM,
			apigen.AuthzSpaceSelector{ExactSpaces: apigen.Some(apigen.SpaceIdList{Values: []uint64{internaldeploy.SpaceID}})}),
		allowRunOnEveryNode(DefaultName, apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_DEFAULT,
			apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: []uint64{}})}),
	}
}
