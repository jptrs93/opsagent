package deployments

import (
	"context"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/domainpolicy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var IngressDomainNotAllowedErr = apigen.NewApiErr("This space may not publish that hostname", "ingress_domain_not_allowed", http.StatusForbidden)

// validateIngressDomains refuses a deployment whose space the domain policies
// do not allow one of its ingress hostnames. It runs under the write lock, as
// policy writes do, so neither can slip past the other.
func validateIngressDomains(ctx context.Context, q *pq.Queries, dep *apigen.Deployment) error {
	hostnames := domainpolicy.Hostnames(&dep.Spec)
	if len(hostnames) == 0 {
		return nil
	}
	rows, err := q.ListSpaceDomainPolicies(ctx)
	if err != nil {
		return err
	}
	policies := make([]apigen.SpaceDomainPolicy, 0, len(rows))
	for _, row := range rows {
		policies = append(policies, row.Value)
	}
	for _, hostname := range hostnames {
		decision := domainpolicy.Decide(policies, dep.SpaceID, hostname)
		if decision.Allowed {
			continue
		}
		space, err := q.GetSpace(ctx, dep.SpaceID)
		if err != nil {
			return err
		}
		e := IngressDomainNotAllowedErr
		e.DisplayErr = "networking.ingress: " + domainpolicy.Refusal(space.Name, hostname, decision)
		return e
	}
	return nil
}
