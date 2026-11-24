// Package domainpolicy decides which spaces may publish which ingress
// hostnames. Rules have the shape of the authz rules: an effect and a
// selector whose positions are an exact list or an all-but list. Enabled
// denies are checked first, then enabled allows, and a hostname no allow
// covers is refused.
package domainpolicy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

const (
	wildcardPrefix = "*."
	maxListValues  = 256
)

// Decision is the outcome for one space and hostname. Policy names the deny
// that refused it and is empty when no allow covers it.
type Decision struct {
	Allowed bool
	Policy  string
}

func Decide(policies []apigen.SpaceDomainPolicy, spaceID uint64, hostname string) Decision {
	allowed := false
	for i := range policies {
		p := &policies[i]
		if p.Disabled || !matches(p.Rule.Selector, spaceID, hostname) {
			continue
		}
		if p.Rule.Effect.Value.Deny != nil {
			return Decision{Policy: p.Name}
		}
		allowed = true
	}
	return Decision{Allowed: allowed}
}

// Refusal words a refused decision for the caller who asked to publish.
func Refusal(space, hostname string, d Decision) string {
	if d.Policy != "" {
		return fmt.Sprintf("space %q may not publish %s: denied by domain policy %q", space, hostname, d.Policy)
	}
	return fmt.Sprintf("space %q may not publish %s: no domain policy allows it", space, hostname)
}

func matches(sel apigen.SpaceDomainSelector, spaceID uint64, hostname string) bool {
	return spaceMatches(sel.Spaces, spaceID) && domainMatches(sel.Domains, hostname)
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

func domainMatches(sel apigen.DomainSelector, hostname string) bool {
	covers := func(pattern string) bool { return patternMatches(pattern, hostname) }
	if sel.ExactDomains.Present {
		return slices.ContainsFunc(sel.ExactDomains.Value.Values, covers)
	}
	if sel.AllDomainsExcluding.Present {
		return !slices.ContainsFunc(sel.AllDomainsExcluding.Value.Values, covers)
	}
	return false
}

// A "*." pattern covers every name below its suffix, not the suffix itself.
func patternMatches(pattern, hostname string) bool {
	if suffix, ok := strings.CutPrefix(pattern, wildcardPrefix); ok {
		return strings.HasSuffix(hostname, "."+suffix)
	}
	return pattern == hostname
}

func canonicalPattern(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if suffix, ok := strings.CutPrefix(value, wildcardPrefix); ok {
		hostname, ok := network.CanonicalHostname(suffix)
		return wildcardPrefix + hostname, ok
	}
	return network.CanonicalHostname(value)
}

// Normalize checks a rule's shape and rewrites its lists in canonical form:
// sorted, without duplicates, domain values lower-cased.
func Normalize(rule *apigen.SpaceDomainRule) error {
	if err := rule.Effect.Value.Validate(); err != nil {
		return fmt.Errorf("effect: exactly one of allow or deny must be set")
	}
	spaces := &rule.Selector.Spaces
	if spaces.ExactSpaces.Present == spaces.AllSpacesExcluding.Present {
		return fmt.Errorf("selector.spaces: exactly one of exact_spaces or all_spaces_excluding must be set")
	}
	if spaces.ExactSpaces.Present && len(spaces.ExactSpaces.Value.Values) == 0 {
		return fmt.Errorf("selector.spaces.exact_spaces must name at least one space")
	}
	for _, list := range []*apigen.Maybe[apigen.SpaceIdList]{&spaces.ExactSpaces, &spaces.AllSpacesExcluding} {
		if !list.Present {
			continue
		}
		if len(list.Value.Values) > maxListValues {
			return fmt.Errorf("selector.spaces lists at most %d spaces", maxListValues)
		}
		values := slices.Clone(list.Value.Values)
		slices.Sort(values)
		list.Value.Values = slices.Compact(values)
		if list.Value.Values == nil {
			list.Value.Values = []uint64{}
		}
	}
	domains := &rule.Selector.Domains
	if domains.ExactDomains.Present == domains.AllDomainsExcluding.Present {
		return fmt.Errorf("selector.domains: exactly one of exact_domains or all_domains_excluding must be set")
	}
	if domains.ExactDomains.Present && len(domains.ExactDomains.Value.Values) == 0 {
		return fmt.Errorf("selector.domains.exact_domains must name at least one domain")
	}
	for _, list := range []*apigen.Maybe[apigen.DomainList]{&domains.ExactDomains, &domains.AllDomainsExcluding} {
		if !list.Present {
			continue
		}
		if len(list.Value.Values) > maxListValues {
			return fmt.Errorf("selector.domains lists at most %d domains", maxListValues)
		}
		values := make([]string, 0, len(list.Value.Values))
		for _, value := range list.Value.Values {
			pattern, ok := canonicalPattern(value)
			if !ok {
				return fmt.Errorf("selector.domains: %q is not a hostname or a \"*.\" wildcard of one", value)
			}
			values = append(values, pattern)
		}
		slices.Sort(values)
		list.Value.Values = slices.Compact(values)
	}
	return nil
}

// NamedSpaces returns the space ids a rule's selector lists in either form.
func NamedSpaces(rule *apigen.SpaceDomainRule) []uint64 {
	spaces := rule.Selector.Spaces
	return append(slices.Clone(spaces.ExactSpaces.Value.Values), spaces.AllSpacesExcluding.Value.Values...)
}

// Hostnames returns the ingress hostnames a spec publishes, sorted and
// without duplicates. Only virtual networking publishes any.
func Hostnames(spec *apigen.DeploymentSpec) []string {
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		return nil
	}
	var out []string
	for _, route := range spec.Networking.Ingress {
		out = append(out, route.Hostname)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// DefaultName is the allow-everything policy a cluster starts with.
const DefaultName = "default_ingress_domains"

func Default() apigen.SpaceDomainPolicy {
	return apigen.SpaceDomainPolicy{
		Name: DefaultName,
		Rule: apigen.SpaceDomainRule{
			Effect: apigen.SpaceDomainEffect{Value: apigen.SpaceDomainEffectValueOneof{Allow: &apigen.SpaceDomainAllow{}}},
			Selector: apigen.SpaceDomainSelector{
				Spaces:  apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: []uint64{}})},
				Domains: apigen.DomainSelector{AllDomainsExcluding: apigen.Some(apigen.DomainList{Values: []string{}})},
			},
		},
	}
}
