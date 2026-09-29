package apigen

import (
	"fmt"
	"net/http"
)

// policyLabel is the name a route policy uses for this session kind. The
// generated mux hands over the route's ANY_OF list of labels; a session
// satisfies it when its own kind is listed.
func (k UserSessionKind) policyLabel() string {
	if k == UserSessionKind_USER_SESSION_KIND_BOOTSTRAP {
		return "bootstrap"
	}
	return "full"
}

func (p AccessPolicy) CanAccess(kind UserSessionKind) error {
	switch p.PolicyType {
	case AccessPolicyType_NO_AUTH, AccessPolicyType_OPTIONAL_AUTH:
		return nil
	case AccessPolicyType_ANY_OF:
		label := kind.policyLabel()
		for _, accepted := range p.Scopes {
			if accepted == label {
				return nil
			}
		}
		return NewApiErr("Unauthorized", fmt.Sprintf("access denied: a %s session cannot use this route", label), http.StatusForbidden)
	default:
		return NewApiErr("Unauthorized", fmt.Sprintf("unsuppored access policy type: %v", p.PolicyType), http.StatusForbidden)
	}
}
