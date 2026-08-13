package apigen

import "testing"

func TestAttributionUserID(t *testing.T) {
	cases := []struct {
		name string
		user *InternalUser
		want int32
	}{
		{"unauthenticated", nil, 0},
		{"direct user", &InternalUser{ID: 7}, 7},
		{"delegated agent", &InternalUser{ID: 7, Delegated: true}, -7},
	}
	for _, c := range cases {
		if got := (Context{User: c.user}).AttributionUserID(); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
