package domainpolicy

import (
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func spaces(exact bool, ids ...uint64) apigen.AuthzSpaceSelector {
	list := apigen.Some(apigen.SpaceIdList{Values: ids})
	if exact {
		return apigen.AuthzSpaceSelector{ExactSpaces: list}
	}
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: list}
}

func domains(exact bool, values ...string) apigen.DomainSelector {
	list := apigen.Some(apigen.DomainList{Values: values})
	if exact {
		return apigen.DomainSelector{ExactDomains: list}
	}
	return apigen.DomainSelector{AllDomainsExcluding: list}
}

func policy(name string, deny bool, s apigen.AuthzSpaceSelector, d apigen.DomainSelector) apigen.SpaceDomainPolicy {
	effect := apigen.SpaceDomainEffectValueOneof{Allow: &apigen.SpaceDomainAllow{}}
	if deny {
		effect = apigen.SpaceDomainEffectValueOneof{Deny: &apigen.SpaceDomainDeny{}}
	}
	return apigen.SpaceDomainPolicy{Name: name, Rule: apigen.SpaceDomainRule{
		Effect:   apigen.SpaceDomainEffect{Value: effect},
		Selector: apigen.SpaceDomainSelector{Spaces: s, Domains: d},
	}}
}

func TestDecide(t *testing.T) {
	const staging, prod = 2, 3
	prodOnly := policy("prod-only", true, spaces(false, prod), domains(true, "example.com", "*.example.com"))
	disabledDeny := policy("off", true, spaces(false), domains(false))
	disabledDeny.Disabled = true
	set := []apigen.SpaceDomainPolicy{Default(), prodOnly, disabledDeny}

	cases := []struct {
		name     string
		policies []apigen.SpaceDomainPolicy
		space    uint64
		hostname string
		want     Decision
	}{
		{"no policy refuses", nil, prod, "example.com", Decision{}},
		{"the default allows anything", []apigen.SpaceDomainPolicy{Default()}, staging, "anything.test", Decision{Allowed: true}},
		{"a deny beats the allow", set, staging, "example.com", Decision{Policy: "prod-only"}},
		{"the excluded space keeps the name", set, prod, "example.com", Decision{Allowed: true}},
		{"a wildcard covers one label below", set, staging, "api.example.com", Decision{Policy: "prod-only"}},
		{"a wildcard covers any depth", set, staging, "a.b.example.com", Decision{Policy: "prod-only"}},
		{"a wildcard is not a suffix match", set, staging, "notexample.com", Decision{Allowed: true}},
		{"a disabled deny is ignored", set, staging, "other.test", Decision{Allowed: true}},
		{"an exact allow covers only its names", []apigen.SpaceDomainPolicy{policy("one", false, spaces(true, staging), domains(true, "a.test"))}, staging, "b.test", Decision{}},
		{"an exact allow covers only its spaces", []apigen.SpaceDomainPolicy{policy("one", false, spaces(true, staging), domains(true, "a.test"))}, prod, "a.test", Decision{}},
		{"all-but domains leaves the listed out", []apigen.SpaceDomainPolicy{policy("rest", false, spaces(false), domains(false, "*.internal.test"))}, prod, "db.internal.test", Decision{}},
	}
	for _, tc := range cases {
		if got := Decide(tc.policies, tc.space, tc.hostname); got != tc.want {
			t.Errorf("%s: Decide = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	bare := policy("bare", false, spaces(true, staging), apigen.DomainSelector{})
	if got := Decide([]apigen.SpaceDomainPolicy{bare}, staging, "a.test"); got.Allowed {
		t.Errorf("a selector with neither list set allowed a hostname")
	}
}

func TestWildcardDoesNotCoverItsOwnSuffix(t *testing.T) {
	only := []apigen.SpaceDomainPolicy{policy("below", false, spaces(false), domains(true, "*.example.com"))}
	if Decide(only, 1, "example.com").Allowed {
		t.Fatal("*.example.com allowed example.com itself")
	}
}

func TestNormalizeCanonicalForm(t *testing.T) {
	rule := policy("p", false, spaces(true, 3, 1, 3), domains(true, " API.Example.com. ", "*.Example.COM", "api.example.com")).Rule
	if err := Normalize(&rule); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := rule.Selector.Spaces.ExactSpaces.Value.Values; !slices.Equal(got, []uint64{1, 3}) {
		t.Errorf("spaces = %v, want [1 3]", got)
	}
	if got := rule.Selector.Domains.ExactDomains.Value.Values; !slices.Equal(got, []string{"*.example.com", "api.example.com"}) {
		t.Errorf("domains = %v, want the two canonical names", got)
	}
}

func TestNormalizeRefusals(t *testing.T) {
	both := apigen.DomainSelector{ExactDomains: apigen.Some(apigen.DomainList{Values: []string{"a.test"}}), AllDomainsExcluding: apigen.Some(apigen.DomainList{})}
	cases := []struct {
		name string
		rule apigen.SpaceDomainRule
		want string
	}{
		{"no effect", apigen.SpaceDomainRule{Selector: apigen.SpaceDomainSelector{Spaces: spaces(false), Domains: domains(false)}}, "effect"},
		{"neither space list", policy("p", false, apigen.AuthzSpaceSelector{}, domains(false)).Rule, "selector.spaces"},
		{"empty exact spaces", policy("p", false, spaces(true), domains(false)).Rule, "at least one space"},
		{"neither domain list", policy("p", false, spaces(false), apigen.DomainSelector{}).Rule, "selector.domains"},
		{"both domain lists", policy("p", false, spaces(false), both).Rule, "selector.domains"},
		{"empty exact domains", policy("p", false, spaces(false), domains(true)).Rule, "at least one domain"},
		{"a bare wildcard", policy("p", false, spaces(false), domains(true, "*")).Rule, "not a hostname"},
		{"a wildcard in the middle", policy("p", false, spaces(false), domains(true, "a.*.test")).Rule, "not a hostname"},
		{"a path", policy("p", false, spaces(false), domains(true, "a.test/prod")).Rule, "not a hostname"},
	}
	for _, tc := range cases {
		err := Normalize(&tc.rule)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Normalize error = %v, want one naming %q", tc.name, err, tc.want)
		}
	}
}

func TestHostnamesOnlyForVirtualNetworking(t *testing.T) {
	ingress := []apigen.Ingress{{Hostname: "b.test"}, {Hostname: "a.test"}, {Hostname: "b.test"}}
	virtual := apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL, Ingress: ingress}}
	if got := Hostnames(&virtual); !slices.Equal(got, []string{"a.test", "b.test"}) {
		t.Errorf("Hostnames = %v, want [a.test b.test]", got)
	}
	host := apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST, Ingress: ingress}}
	if got := Hostnames(&host); len(got) != 0 {
		t.Errorf("host networking published %v", got)
	}
}

func TestDefaultPolicyIsValid(t *testing.T) {
	p := Default()
	if err := Normalize(&p.Rule); err != nil {
		t.Fatalf("Normalize(Default): %v", err)
	}
	if _, err := p.EncodeChecked(); err != nil {
		t.Fatalf("Default does not encode: %v", err)
	}
}
