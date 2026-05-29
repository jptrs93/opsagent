package jwtu

import (
	"fmt"
	"time"
)

func ExpiryFromClaims(claims map[string]any) (time.Time, error) {
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, fmt.Errorf("missing exp claim")
	}
	return time.Unix(int64(exp), 0), nil
}

func ScopesFromClaims(claims map[string]any) []string {
	if direct, ok := claims["scopes"].([]string); ok {
		return append([]string(nil), direct...)
	}
	scopesRaw, _ := claims["scopes"].([]any)
	scopes := make([]string, 0, len(scopesRaw))
	for _, s := range scopesRaw {
		if str, ok := s.(string); ok {
			scopes = append(scopes, str)
		}
	}
	return scopes
}
