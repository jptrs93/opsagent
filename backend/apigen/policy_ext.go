package apigen

import (
	"fmt"
	"net/http"
)

func (p AccessPolicy) CanAccess(userScopes []string) error {
	switch p.PolicyType {
	case AccessPolicyType_NO_AUTH, AccessPolicyType_OPTIONAL_AUTH:
		return nil
	case AccessPolicyType_ANY_OF:
		for _, desired := range p.Scopes {
			for _, granted := range userScopes {
				if desired == granted {
					return nil
				}
			}
		}
		return NewApiErr("Unauthorized", fmt.Sprintf("access denied: user has scopes '%v' but needs one of '%v", userScopes, p.Scopes), http.StatusForbidden)
	default:
		return NewApiErr("Unauthorized", fmt.Sprintf("unsuppored access policy type: %v", p.PolicyType), http.StatusForbidden)
	}
}
