// Package spacedomainpolicies stores the cluster's space domain policies and
// keeps them consistent with the ingress routes deployments already hold: a
// policy write that would leave a stored route unpermitted is refused.
package spacedomainpolicies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/domainpolicy"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidErr = apigen.NewApiErr("Invalid domain policy", "invalid_space_domain_policy", http.StatusBadRequest)
var NotFoundErr = apigen.NewApiErr("Domain policy not found", "space_domain_policy_not_found", http.StatusNotFound)
var VersionConflictErr = apigen.NewApiErr("Domain policy was modified concurrently", "space_domain_policy_version_conflict", http.StatusConflict)
var DuplicateNameErr = apigen.NewApiErr("A domain policy with this name already exists", "space_domain_policy_duplicate_name", http.StatusConflict)
var SpaceNotFoundErr = apigen.NewApiErr("Domain policy names a space that does not exist", "space_domain_policy_space_not_found", http.StatusNotFound)
var RoutesInUseErr = apigen.NewApiErr("Domain policies must keep every published hostname allowed", "space_domain_policy_routes_in_use", http.StatusConflict)

func invalidf(format string, args ...any) error {
	e := InvalidErr
	e.DisplayErr = "Invalid domain policy: " + fmt.Sprintf(format, args...)
	return e
}

// Normalize checks the policy's shape and puts its lists in canonical form.
func Normalize(policy *apigen.SpaceDomainPolicy) error {
	policy.Name = strings.TrimSpace(policy.Name)
	if policy.Name == "" || len(policy.Name) > 64 {
		return invalidf("name must be 1 to 64 characters")
	}
	if err := domainpolicy.Normalize(&policy.Rule); err != nil {
		return invalidf("%s", err)
	}
	return nil
}

func ByID(q *pq.Queries, id uint64) *pq.SpaceDomainPolicyEvent {
	row, err := q.GetSpaceDomainPolicy(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return row
}

// Values lists the live policies in id order.
func Values(ctx context.Context, q *pq.Queries) ([]apigen.SpaceDomainPolicy, error) {
	rows, err := q.ListSpaceDomainPolicies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.SpaceDomainPolicy, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Value)
	}
	return out, nil
}

func view(seq, now, createdTime, author int64, id uint64, policy apigen.SpaceDomainPolicy) *pq.SpaceDomainPolicyEvent {
	policy.ID = id
	return &pq.SpaceDomainPolicyEvent{SpaceDomainPolicyID: id, Seq: seq, Author: author, CreatedTime: createdTime, EventTime: now, Value: policy}
}

func Create(store *state.Service, author int64, policy *apigen.SpaceDomainPolicy) (*pq.SpaceDomainPolicyEvent, error) {
	ctx := context.Background()
	var created *pq.SpaceDomainPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := Values(ctx, q)
		if err != nil {
			return nil, err
		}
		if err := validateWrite(ctx, q, current, 0, policy); err != nil {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		created = view(seq, now, now, author, id, *policy)
		return pq.NewUpdate(created.Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces the policy as long as it has no write newer than
// expectedSeq; zero skips the check.
func Update(store *state.Service, id uint64, expectedSeq int64, author int64, policy *apigen.SpaceDomainPolicy) (*pq.SpaceDomainPolicyEvent, error) {
	ctx := context.Background()
	var updated *pq.SpaceDomainPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if expectedSeq != 0 && prev.Seq > expectedSeq {
			return nil, VersionConflictErr
		}
		current, err := Values(ctx, q)
		if err != nil {
			return nil, err
		}
		if err := validateWrite(ctx, q, current, id, policy); err != nil {
			return nil, err
		}
		updated = view(seq, time.Now().UnixMilli(), prev.CreatedTime, author, id, *policy)
		return pq.NewUpdate(updated.Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func Delete(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := livePrevious(ctx, q, id); err != nil {
			return nil, err
		}
		current, err := Values(ctx, q)
		if err != nil {
			return nil, err
		}
		if err := requireRoutesAllowed(ctx, q, without(current, id)); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY, id)), nil
	})
}

// SeedDefault creates the allow-everything policy unless the write log has
// ever carried one by that name, so an operator's deletion sticks.
func SeedDefault(store *state.Service) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		seen, err := q.SpaceDomainPolicyNameEverLogged(ctx, domainpolicy.DefaultName)
		if err != nil || seen {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SPACE_DOMAIN_POLICY)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		return pq.NewUpdate(view(seq, now, now, 0, id, domainpolicy.Default()).Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
	})
}

func livePrevious(ctx context.Context, q *pq.Queries, id uint64) (*pq.SpaceDomainPolicyEvent, error) {
	prev, err := q.GetSpaceDomainPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return prev, nil
}

func without(policies []apigen.SpaceDomainPolicy, id uint64) []apigen.SpaceDomainPolicy {
	out := make([]apigen.SpaceDomainPolicy, 0, len(policies))
	for _, p := range policies {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

// validateWrite runs under the write lock: the policy replaces the one with
// its id, or joins the set when id is zero.
func validateWrite(ctx context.Context, q *pq.Queries, current []apigen.SpaceDomainPolicy, id uint64, policy *apigen.SpaceDomainPolicy) error {
	others := without(current, id)
	for _, other := range others {
		if other.Name == policy.Name {
			return DuplicateNameErr
		}
	}
	for _, spaceID := range domainpolicy.NamedSpaces(&policy.Rule) {
		if _, err := q.GetSpace(ctx, spaceID); errors.Is(err, sql.ErrNoRows) {
			return SpaceNotFoundErr
		} else if err != nil {
			return err
		}
	}
	next := *policy
	next.ID = id
	return requireRoutesAllowed(ctx, q, append(others, next))
}

// requireRoutesAllowed refuses a policy set under which a hostname some
// deployment already publishes would no longer be allowed.
func requireRoutesAllowed(ctx context.Context, q *pq.Queries, policies []apigen.SpaceDomainPolicy) error {
	deployments, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return err
	}
	spaceNames := map[uint64]string{}
	var stranded []string
	for _, record := range deployments {
		if internaldeploy.IsInternalConfig(record) {
			continue
		}
		dep := &record.Deployment
		for _, hostname := range domainpolicy.Hostnames(&dep.Spec) {
			decision := domainpolicy.Decide(policies, dep.SpaceID, hostname)
			if decision.Allowed {
				continue
			}
			name, ok := spaceNames[dep.SpaceID]
			if !ok {
				space, err := q.GetSpace(ctx, dep.SpaceID)
				if err != nil {
					return err
				}
				name = space.Name
				spaceNames[dep.SpaceID] = name
			}
			stranded = append(stranded, fmt.Sprintf("%s of deployment %q in space %q", hostname, dep.Name, name))
		}
	}
	if len(stranded) == 0 {
		return nil
	}
	const maxShown = 5
	total := len(stranded)
	if total > maxShown {
		stranded = append(stranded[:maxShown:maxShown], fmt.Sprintf("%d more", total-maxShown))
	}
	e := RoutesInUseErr
	e.DisplayErr = "This change would leave published hostnames without permission: " + strings.Join(stranded, "; ")
	return e
}
