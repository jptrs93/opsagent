package apigen

import "testing"

func TestAttributionUserID(t *testing.T) {
	cases := []struct {
		name      string
		user      *User
		delegated bool
		want      int64
	}{
		{"unauthenticated", nil, false, 0},
		{"direct user", &User{ID: 7}, false, 7},
		{"delegated agent", &User{ID: 7}, true, -7},
	}
	for _, c := range cases {
		if got := (Context{User: c.user, Delegated: c.delegated}).AttributionUserID(); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
