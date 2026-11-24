package webuihandler

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/spacedomainpolicies"
	"github.com/jptrs93/opsagent/backend/lib/domainpolicy"
)

func newDomainPolicyHandler(t *testing.T) (*Handler, *apigen.Space, *nodes.Node) {
	t.Helper()
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	seedDefaultDomainPolicy(t, h.Store)
	return h, staging, node
}

func domainRule(deny bool, spaces apigen.AuthzSpaceSelector, domains apigen.DomainSelector) apigen.SpaceDomainRule {
	effect := apigen.SpaceDomainEffectValueOneof{Allow: &apigen.SpaceDomainAllow{}}
	if deny {
		effect = apigen.SpaceDomainEffectValueOneof{Deny: &apigen.SpaceDomainDeny{}}
	}
	return apigen.SpaceDomainRule{Effect: apigen.SpaceDomainEffect{Value: effect}, Selector: apigen.SpaceDomainSelector{Spaces: spaces, Domains: domains}}
}

func exactDomains(values ...string) apigen.DomainSelector {
	return apigen.DomainSelector{ExactDomains: apigen.Some(apigen.DomainList{Values: values})}
}

func allDomains() apigen.DomainSelector {
	return apigen.DomainSelector{AllDomainsExcluding: apigen.Some(apigen.DomainList{Values: []string{}})}
}

func allSpacesBut(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func domainPolicyOf(u *apigen.CoreWriteUpdate) *apigen.SpaceDomainPolicy {
	m := writtenMutation(u, apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY)
	if m == nil {
		return nil
	}
	return m.Entity().Value.SpaceDomainPolicy
}

func createDomainPolicy(t *testing.T, h *Handler, name string, rule apigen.SpaceDomainRule) *apigen.SpaceDomainPolicy {
	t.Helper()
	u, err := h.PostV1SpaceDomainPoliciesCreate(enforceCtx(1, false), &apigen.SpaceDomainPolicyCreateRequest{Name: name, Rule: rule})
	if err != nil {
		t.Fatalf("creating domain policy %q: %v", name, err)
	}
	return domainPolicyOf(u)
}

func defaultDomainPolicy(t *testing.T, h *Handler) *apigen.SpaceDomainPolicy {
	t.Helper()
	rows, err := h.Queries.ListSpaceDomainPolicies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Value.Name == domainpolicy.DefaultName {
			return &row.Value
		}
	}
	t.Fatal("the default domain policy is missing")
	return nil
}

func setDomainPolicyDisabled(h *Handler, p *apigen.SpaceDomainPolicy, disabled bool) error {
	_, err := h.PostV1SpaceDomainPoliciesUpdate(enforceCtx(1, false), &apigen.SpaceDomainPolicyUpdateRequest{ID: p.ID, Name: p.Name, Disabled: disabled, Rule: p.Rule})
	return err
}

func publishRequest(spaceID, nodeID uint64, name, hostname string) *apigen.DeploymentCreateRequest {
	return &apigen.DeploymentCreateRequest{
		SpaceID: spaceID, Name: name,
		Scheduling: apigen.DedicatedScheduling(false, nodeID),
		Spec: remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
			Mode:    apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []apigen.Ingress{tlsPassthroughIngressOnPort(hostname, 5432, 8443)},
		}),
	}
}

func displayOf(err error) string {
	var apiErr apigen.ApiErr
	if errors.As(err, &apiErr) {
		return apiErr.DisplayErr
	}
	return ""
}

func TestDefaultDomainPolicySeededOnce(t *testing.T) {
	h, _, _ := newDomainPolicyHandler(t)
	p := defaultDomainPolicy(t, h)
	if p.Disabled || p.Rule.Effect.Value.Allow == nil {
		t.Fatalf("default policy = %+v, want an enabled allow", p)
	}
	seedDefaultDomainPolicy(t, h.Store)
	if rows, _ := h.Queries.ListSpaceDomainPolicies(t.Context()); len(rows) != 1 {
		t.Fatalf("a second seed left %d policies, want 1", len(rows))
	}
	if err := h.PostV1SpaceDomainPoliciesDelete(enforceCtx(1, false), &apigen.SpaceDomainPolicyDeleteRequest{ID: p.ID}); err != nil {
		t.Fatalf("deleting the default with nothing published: %v", err)
	}
	seedDefaultDomainPolicy(t, h.Store)
	if rows, _ := h.Queries.ListSpaceDomainPolicies(t.Context()); len(rows) != 0 {
		t.Fatalf("the deleted default was seeded again: %d policies", len(rows))
	}
}

func TestDomainPolicyDenyRefusesTheSave(t *testing.T) {
	h, staging, node := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	createDomainPolicy(t, h, "staging-names", domainRule(true, allSpacesBut(staging.ID), exactDomains("example.com", "*.example.com")))

	for _, hostname := range []string{"example.com", "api.example.com", "a.b.example.com"} {
		_, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "squat", hostname))
		if !errors.Is(err, deployments.IngressDomainNotAllowedErr) {
			t.Fatalf("global space publishing %s: err = %v, want IngressDomainNotAllowedErr", hostname, err)
		}
		if msg := displayOf(err); !strings.Contains(msg, `"staging-names"`) || !strings.Contains(msg, hostname) {
			t.Fatalf("refusal %q does not name the policy and hostname", msg)
		}
	}
	if _, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "other", "notexample.com")); err != nil {
		t.Fatalf("a name the deny does not cover was refused: %v", err)
	}
	if _, err := h.deploymentsCreate(admin, publishRequest(staging.ID, node.ID, "web", "api.example.com")); err != nil {
		t.Fatalf("the excluded space was refused its own name: %v", err)
	}
}

func TestNoAllowRefusesIngressButNotHostNetworking(t *testing.T) {
	h, _, node := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	if err := setDomainPolicyDisabled(h, defaultDomainPolicy(t, h), true); err != nil {
		t.Fatalf("disabling the default with nothing published: %v", err)
	}
	_, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "web", "web.example.com"))
	if !errors.Is(err, deployments.IngressDomainNotAllowedErr) || !strings.Contains(displayOf(err), "no domain policy allows it") {
		t.Fatalf("publishing with no enabled allow: err = %v (%q)", err, displayOf(err))
	}
	if _, err := h.deploymentsCreate(admin, &apigen.DeploymentCreateRequest{
		SpaceID: nodes.DefaultSpaceID, Name: "plain", Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec: remoteDeploymentSpec("nginx", hostNetworking()),
	}); err != nil {
		t.Fatalf("a deployment without ingress was refused: %v", err)
	}
}

func TestDomainPolicyWritesKeepStoredRoutesAllowed(t *testing.T) {
	h, staging, node := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	if _, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "db", "db.example.com")); err != nil {
		t.Fatalf("publishing under the default: %v", err)
	}
	def := defaultDomainPolicy(t, h)
	stranding := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, spacedomainpolicies.RoutesInUseErr) {
			t.Fatalf("%s: err = %v, want RoutesInUseErr", what, err)
		}
		if msg := displayOf(err); !strings.Contains(msg, "db.example.com") || !strings.Contains(msg, `"db"`) || !strings.Contains(msg, `"global"`) {
			t.Fatalf("%s: refusal %q does not name the hostname, deployment and space", what, msg)
		}
	}

	_, err := h.PostV1SpaceDomainPoliciesCreate(admin, &apigen.SpaceDomainPolicyCreateRequest{Name: "lock", Rule: domainRule(true, allSpacesBut(staging.ID), exactDomains("*.example.com"))})
	stranding("a deny over a published name", err)
	stranding("disabling the only allow", setDomainPolicyDisabled(h, def, true))
	stranding("deleting the only allow", h.PostV1SpaceDomainPoliciesDelete(admin, &apigen.SpaceDomainPolicyDeleteRequest{ID: def.ID}))
	narrowed := def.Rule
	narrowed.Selector.Spaces = exactSpaces(staging.ID)
	_, err = h.PostV1SpaceDomainPoliciesUpdate(admin, &apigen.SpaceDomainPolicyUpdateRequest{ID: def.ID, Name: def.Name, Rule: narrowed})
	stranding("narrowing the only allow", err)
	if rows, _ := h.Queries.ListSpaceDomainPolicies(t.Context()); len(rows) != 1 || rows[0].Value.Disabled {
		t.Fatalf("a refused write changed the policies: %+v", rows)
	}

	createDomainPolicy(t, h, "db-name", domainRule(false, exactSpaces(nodes.DefaultSpaceID), exactDomains("db.example.com")))
	if err := setDomainPolicyDisabled(h, def, true); err != nil {
		t.Fatalf("disabling the default once another allow covers the route: %v", err)
	}
	if _, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "other", "other.example.com")); !errors.Is(err, deployments.IngressDomainNotAllowedErr) {
		t.Fatalf("a name outside the remaining allow: err = %v, want IngressDomainNotAllowedErr", err)
	}
}

func TestSpaceMoveChecksTheDestinationSpace(t *testing.T) {
	h, staging, node := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	cfg, err := h.deploymentsCreate(admin, publishRequest(nodes.DefaultSpaceID, node.ID, "db", "db.example.com"))
	if err != nil {
		t.Fatalf("publishing under the default: %v", err)
	}
	createDomainPolicy(t, h, "global-only", domainRule(true, allSpacesBut(nodes.DefaultSpaceID), exactDomains("db.example.com")))
	_, err = h.deploymentsUpdate(admin, &apigen.DeploymentUpdateRequest{
		DeploymentID: cfg.Deployment.ID, ExpectedSeq: cfg.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}},
	})
	if !errors.Is(err, deployments.IngressDomainNotAllowedErr) {
		t.Fatalf("moving a published hostname into a denied space: err = %v, want IngressDomainNotAllowedErr", err)
	}
}

func TestDomainPolicyValidation(t *testing.T) {
	h, staging, _ := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	create := func(name string, rule apigen.SpaceDomainRule) error {
		_, err := h.PostV1SpaceDomainPoliciesCreate(admin, &apigen.SpaceDomainPolicyCreateRequest{Name: name, Rule: rule})
		return err
	}
	if err := create("", domainRule(false, allSpacesBut(), allDomains())); !errors.Is(err, spacedomainpolicies.InvalidErr) {
		t.Fatalf("empty name: err = %v, want InvalidErr", err)
	}
	if err := create("bad", domainRule(false, allSpacesBut(), exactDomains("not a host"))); !errors.Is(err, spacedomainpolicies.InvalidErr) {
		t.Fatalf("bad hostname: err = %v, want InvalidErr", err)
	}
	if err := create("bad", apigen.SpaceDomainRule{Selector: apigen.SpaceDomainSelector{Spaces: allSpacesBut(), Domains: allDomains()}}); !errors.Is(err, spacedomainpolicies.InvalidErr) {
		t.Fatalf("no effect: err = %v, want InvalidErr", err)
	}
	if err := create("ghost", domainRule(false, exactSpaces(999), allDomains())); !errors.Is(err, spacedomainpolicies.SpaceNotFoundErr) {
		t.Fatalf("unknown space: err = %v, want SpaceNotFoundErr", err)
	}
	if err := create(domainpolicy.DefaultName, domainRule(false, allSpacesBut(), allDomains())); !errors.Is(err, spacedomainpolicies.DuplicateNameErr) {
		t.Fatalf("duplicate name: err = %v, want DuplicateNameErr", err)
	}

	created := createDomainPolicy(t, h, " team ", domainRule(false, exactSpaces(staging.ID, nodes.DefaultSpaceID, staging.ID), exactDomains("B.Example.com.", "a.example.com", "b.example.com")))
	if created.Name != "team" {
		t.Fatalf("name = %q, want it trimmed", created.Name)
	}
	if got := created.Rule.Selector.Spaces.ExactSpaces.Value.Values; !slices.Equal(got, []uint64{nodes.DefaultSpaceID, staging.ID}) {
		t.Fatalf("spaces = %v, want them sorted without duplicates", got)
	}
	if got := created.Rule.Selector.Domains.ExactDomains.Value.Values; !slices.Equal(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("domains = %v, want canonical names", got)
	}

	row := spacedomainpolicies.ByID(h.Queries, created.ID)
	if _, err := h.PostV1SpaceDomainPoliciesUpdate(admin, &apigen.SpaceDomainPolicyUpdateRequest{ID: created.ID, ExpectedSeq: row.Seq, Name: "team", Disabled: true, Rule: created.Rule}); err != nil {
		t.Fatalf("update at the current seq: %v", err)
	}
	if _, err := h.PostV1SpaceDomainPoliciesUpdate(admin, &apigen.SpaceDomainPolicyUpdateRequest{ID: created.ID, ExpectedSeq: row.Seq, Name: "team", Rule: created.Rule}); !errors.Is(err, spacedomainpolicies.VersionConflictErr) {
		t.Fatalf("update at a stale seq: err = %v, want VersionConflictErr", err)
	}
}

func TestDomainPolicyAccess(t *testing.T) {
	h, _, _ := newDomainPolicyHandler(t)
	admin, agent, spaceop := enforceCtx(1, false), enforceCtx(1, true), enforceCtx(2, false)
	def := defaultDomainPolicy(t, h)
	rule := domainRule(false, allSpacesBut(), exactDomains("a.example.com"))

	for name, ctx := range map[string]apigen.Context{"space admin": spaceop, "agent session": agent} {
		if _, err := h.PostV1SpaceDomainPoliciesCreate(ctx, &apigen.SpaceDomainPolicyCreateRequest{Name: "mine", Rule: rule}); !errors.Is(err, AccessDeniedErr) {
			t.Fatalf("%s create: err = %v, want AccessDeniedErr", name, err)
		}
		if _, err := h.PostV1SpaceDomainPoliciesUpdate(ctx, &apigen.SpaceDomainPolicyUpdateRequest{ID: def.ID, Name: def.Name, Disabled: true, Rule: def.Rule}); !errors.Is(err, spacedomainpolicies.NotFoundErr) {
			t.Fatalf("%s update: err = %v, want NotFoundErr", name, err)
		}
		if err := h.PostV1SpaceDomainPoliciesDelete(ctx, &apigen.SpaceDomainPolicyDeleteRequest{ID: def.ID}); !errors.Is(err, spacedomainpolicies.NotFoundErr) {
			t.Fatalf("%s delete: err = %v, want NotFoundErr", name, err)
		}
		if seen := visibleOpening(t, h, ctx)[apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY]; len(seen) != 0 {
			t.Fatalf("%s sees %d domain policies in the stream", name, len(seen))
		}
	}
	if seen := visibleOpening(t, h, admin)[apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY]; len(seen) != 1 {
		t.Fatalf("the cluster admin sees %d domain policies, want 1", len(seen))
	}
	if err := h.PostV1SpaceDomainPoliciesDelete(admin, &apigen.SpaceDomainPolicyDeleteRequest{ID: 999}); !errors.Is(err, spacedomainpolicies.NotFoundErr) {
		t.Fatalf("deleting an unknown policy: err = %v, want NotFoundErr", err)
	}
}

func TestSpaceDeleteRefusedWhileADomainPolicyNamesIt(t *testing.T) {
	h, staging, _ := newDomainPolicyHandler(t)
	admin := enforceCtx(1, false)
	p := createDomainPolicy(t, h, "staging-names", domainRule(false, exactSpaces(staging.ID), exactDomains("a.example.com")))
	err := h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: staging.ID})
	if msg := displayOf(err); !strings.Contains(msg, "1 domain policy") {
		t.Fatalf("space delete: err = %v (%q), want it to name the domain policy", err, msg)
	}
	if err := h.PostV1SpaceDomainPoliciesDelete(admin, &apigen.SpaceDomainPolicyDeleteRequest{ID: p.ID}); err != nil {
		t.Fatalf("deleting the policy: %v", err)
	}
	if err := h.PostV1SpacesDelete(admin, &apigen.SpaceDeleteRequest{ID: staging.ID}); err != nil {
		t.Fatalf("space delete once nothing names it: %v", err)
	}
}
